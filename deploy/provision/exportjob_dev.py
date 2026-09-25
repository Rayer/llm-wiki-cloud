#!/usr/bin/env python3
"""First-time DEV Export Job provisioning; run only from the reviewed develop workflow."""
from __future__ import annotations

import argparse
import ast
import copy
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import time

ROOT = Path(__file__).resolve().parents[2]
CONTRACT = ROOT / "deploy/provision/exportjob-dev.json"
EVIDENCE = ROOT / ".provision/exportjob-dev-evidence.json"
MAX_ETAG_ATTEMPTS = 5
IMAGE_REPO = "llm-wiki-bff-export-job"
BUILD_ID_RE = re.compile(r"\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b", re.I)
DB_CONDITION = "resource.name == 'projects/llm-wiki-cloud/databases/llm-wiki-cloud-dev'"
STORAGE_USERS = (
    "resource.type == 'storage.googleapis.com/Object' && "
    "resource.name.startsWith('projects/_/buckets/llm-wiki-data-dev/objects/users/')"
)
STORAGE_EXPORTS = (
    "resource.type == 'storage.googleapis.com/Object' && "
    "(resource.name.startsWith('projects/_/buckets/llm-wiki-data-dev/objects/exports/tmp/') || "
    "resource.name.startsWith('projects/_/buckets/llm-wiki-data-dev/objects/exports/ready/'))"
)
STORAGE_READY_ARCHIVES = (
    "resource.type == 'storage.googleapis.com/Object' && "
    "resource.name.startsWith('projects/_/buckets/llm-wiki-data-dev/objects/exports/ready/')"
)
DEPLOYER = "serviceAccount:gh-actions-bff-deployer@llm-wiki-cloud.iam.gserviceaccount.com"
# Read grants are read-only; project getIamPolicy exposes all project IAM metadata, but no secrets or write authority.
VERIFIER_ROLES = {
    "roleReadback": ("lwcExportRoleReadback", "LWC Export role readback", ["iam.roles.get"]),
    "projectPolicyReadback": ("lwcExportProjectPolicyReadback", "LWC Export project IAM readback",
                               ["resourcemanager.projects.getIamPolicy"]),
    "signerPolicyReadback": ("lwcExportSignerPolicyReadback", "LWC Export signer IAM readback",
                             ["iam.serviceAccounts.getIamPolicy"]),
}


class ProvisionError(RuntimeError):
    pass


def load_contract(path: Path = CONTRACT) -> dict:
    value = json.loads(path.read_text())
    if (value.get("environment"), value.get("project"), value.get("region"), value.get("bucket"),
            value.get("database")) != ("development", "llm-wiki-cloud", "asia-east1", "llm-wiki-data-dev", "llm-wiki-cloud-dev"):
        raise ProvisionError("provisioning contract is not the fixed DEV target")
    if value.get("max_retries") != 0 or value.get("parallelism") != 1 or value.get("tasks") != 1 or value.get("job_timeout") != "23h":
        raise ProvisionError("provisioning Job limits differ from the reviewed DEV contract")
    if value.get("storage_roles") != {
            "objectLister": ["storage.objects.list"],
            "sourceReader": ["storage.objects.get"],
            "archiveWriter": ["storage.objects.create", "storage.objects.delete", "storage.objects.get", "storage.objects.update"],
            "blobSigner": ["iam.serviceAccounts.signBlob"],
            "readyArchiveReader": ["storage.objects.get"]}:
        raise ProvisionError("storage custom roles differ from the reviewed least-privilege contract")
    for key, expected in (("job", "export-job-dev"),
                          ("runtime_service_account", "lwc-export-worker-dev@llm-wiki-cloud.iam.gserviceaccount.com"),
                          ("signing_service_account", "lwc-export-signer-dev@llm-wiki-cloud.iam.gserviceaccount.com"),
                          ("bff_service_account", "lwc-bff-dev@llm-wiki-cloud.iam.gserviceaccount.com")):
        if value.get(key) != expected:
            raise ProvisionError(f"provisioning contract has an unexpected {key}")
    return value


def _run(args: list[str], *, input_text: str | None = None) -> subprocess.CompletedProcess[str]:
    return subprocess.run(args, cwd=ROOT, text=True, input=input_text, capture_output=True)


def _etag_conflict(result: subprocess.CompletedProcess[str]) -> bool:
    text = (result.stdout + result.stderr).lower()
    return result.returncode != 0 and any(token in text for token in ("etag", "conditionnotmet", "precondition failed", "http 412", "status code: 412"))


def _redacted_diagnostic(value: str) -> str:
    value = re.sub(r"-----BEGIN [^-]+-----.*?-----END [^-]+-----", "[REDACTED PRIVATE KEY]", value, flags=re.S)
    value = re.sub(r"(?i)(authorization\s*:\s*bearer\s+)[^\s]+", r"\1[REDACTED]", value)
    value = re.sub(r"(?i)\b([A-Z0-9_]*(?:TOKEN|SECRET|PASSWORD|CREDENTIAL|PRIVATE_KEY)[A-Z0-9_]*\s*[=:]\s*)(?:\"[^\"]*\"|'[^']*'|[^\s,;]+)", r"\1[REDACTED]", value)
    value = re.sub(r"\bya29\.[A-Za-z0-9._~-]+", "[REDACTED TOKEN]", value)
    value = re.sub(r"\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+", "[REDACTED JWT]", value)
    value = re.sub(r"(?i)(https?://)[^\s/@:]+:[^\s/@]+@", r"\1[REDACTED]@", value)
    return " ".join(value.split())[:500]


def _is_absent_describe(args: tuple[str, ...], output: str) -> bool:
    """Recognize only provider not-found responses for supported resource describes."""
    message = output.lower()
    operation = args[:3]
    supported = {("run", "jobs", "describe"), ("iam", "service-accounts", "describe"), ("iam", "roles", "describe")}
    if operation not in supported:
        return False
    if re.search(r"permission_denied|permission denied|access denied|unauthenticated|authentication|forbidden|timed? out|timeout|connection|transport|unavailable|deadline exceeded|\b(?:401|403)\b", message):
        return False
    if "not_found" in message or "not found" in message:
        return True
    expected = {"run": "job", "iam": "service account" if operation[1] == "service-accounts" else "role"}[operation[0]]
    return re.search(rf"cannot find {expected} \[[^\]]+\]", message) is not None


def _resource_iam_source_signature(source: str) -> str:
    tree = ast.parse(source)
    functions = {"load_contract", "ready_archive_binding"}
    methods = {"image_and_job_config", "job_matches", "ensure_job", "apply_job_iam", "owner_prerequisites",
               "verify_policy", "ensure_custom_role", "ensure_verifier_roles", "apply_iam", "apply_verifier_iam",
               "verify_service_account", "verify_custom_role", "verify_verifier_roles", "bucket_ready"}
    constants = {"DB_CONDITION", "STORAGE_USERS", "STORAGE_EXPORTS", "STORAGE_READY_ARCHIVES", "DEPLOYER", "VERIFIER_ROLES"}
    selected = []
    for node in tree.body:
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and node.name in functions:
            selected.append(ast.dump(node, include_attributes=False))
        elif isinstance(node, (ast.Assign, ast.AnnAssign)):
            targets = node.targets if isinstance(node, ast.Assign) else [node.target]
            if any(isinstance(target, ast.Name) and target.id in constants for target in targets):
                selected.append(ast.dump(node, include_attributes=False))
        elif isinstance(node, ast.ClassDef) and node.name == "Provisioner":
            for method in node.body:
                if not isinstance(method, (ast.FunctionDef, ast.AsyncFunctionDef)) or method.name not in methods:
                    continue
                method = copy.deepcopy(method)
                if method.name == "job_matches":
                    assignments = [item for item in ast.walk(method) if isinstance(item, ast.Assign) and
                                   any(isinstance(target, ast.Name) and target.id == "timeout_matches" for target in item.targets)]
                    if len(assignments) != 1:
                        selected.append("invalid timeout normalization")
                        continue
                    assignments[0].value = ast.Constant("approved timeout representation comparison")
                selected.append(ast.dump(method, include_attributes=False))
    return "\n".join(selected)


def _timeout_normalizer_is_approved(source: str) -> bool:
    try:
        tree = ast.parse(source)
        matches = next(node for node in ast.walk(tree) if isinstance(node, ast.FunctionDef) and node.name == "_timeout_matches")
    except (SyntaxError, StopIteration):
        return False
    expected = """def _timeout_matches(value: object, expected: str) -> bool:
    seconds = {\"23h\": 82800}.get(expected)
    if seconds is None or isinstance(value, bool):
        return False
    if isinstance(value, int):
        return value == seconds
    if not isinstance(value, str):
        return False
    if value == expected:
        return True
    duration = re.fullmatch(r\"(\\d+)([hms])\", value)
    if duration:
        scale = {\"h\": 3600, \"m\": 60, \"s\": 1}[duration.group(2)]
        return int(duration.group(1)) * scale == seconds
    return value.isdecimal() and int(value) == seconds
"""
    expected_node = ast.parse(expected).body[0]
    return ast.dump(matches, include_attributes=False) == ast.dump(expected_node, include_attributes=False)


def _timeout_source_repair_is_approved(owner_source: str, current_source: str) -> bool:
    def expression(source: str) -> ast.expr | None:
        try:
            tree = ast.parse(source)
        except SyntaxError:
            return None
        cls = next((node for node in tree.body if isinstance(node, ast.ClassDef) and node.name == "Provisioner"), None)
        method = next((node for node in cls.body if isinstance(node, ast.FunctionDef) and node.name == "job_matches"), None) if cls else None
        assignments = [item for item in ast.walk(method) if isinstance(item, ast.Assign) and
                       any(isinstance(target, ast.Name) and target.id == "timeout_matches" for target in item.targets)] if method else []
        return assignments[0].value if len(assignments) == 1 else None

    old, new = expression(owner_source), expression(current_source)
    expected_old = ast.parse('timeout in (expected["timeout"], "82800s", 82800)', mode="eval").body
    expected_new = ast.parse('_timeout_matches(timeout, expected["timeout"])', mode="eval").body
    return (old is not None and new is not None and
            ast.dump(old, include_attributes=False) == ast.dump(expected_old, include_attributes=False) and
            ast.dump(new, include_attributes=False) == ast.dump(expected_new, include_attributes=False))


def _timeout_matches(value: object, expected: str) -> bool:
    seconds = {"23h": 82800}.get(expected)
    if seconds is None or isinstance(value, bool):
        return False
    if isinstance(value, int):
        return value == seconds
    if not isinstance(value, str):
        return False
    if value == expected:
        return True
    duration = re.fullmatch(r"(\d+)([hms])", value)
    if duration:
        scale = {"h": 3600, "m": 60, "s": 1}[duration.group(2)]
        return int(duration.group(1)) * scale == seconds
    return value.isdecimal() and int(value) == seconds


def _build_id_from(result: subprocess.CompletedProcess[str]) -> str | None:
    try:
        payload = json.loads(result.stdout)
        if isinstance(payload, dict) and isinstance(payload.get("id"), str):
            value = payload["id"]
            if BUILD_ID_RE.fullmatch(value):
                return value
    except (json.JSONDecodeError, TypeError):
        pass
    match = BUILD_ID_RE.search(result.stdout)
    return match.group(0) if match else None


def ready_archive_binding(role: str, signing_service_account: str) -> dict:
    return {"role": role, "member": "serviceAccount:" + signing_service_account, "condition": {
        "title": "lwc344-export-ready-archive-read", "description": "Allow signed URL GETs for ready archives only",
        "expression": STORAGE_READY_ARCHIVES}}


class Provisioner:
    def __init__(self, config: dict, run=_run, evidence_path: Path = EVIDENCE):
        self.c = config
        self.run = run
        self.evidence_path = evidence_path
        fresh = {
            "schema": "lwc-344-exportjob-dev-provision-v2",
            "source": {"ref": os.getenv("SOURCE_REF", ""), "sha": os.getenv("SOURCE_SHA", "")},
            "target": {"environment": config["environment"], "project": config["project"], "region": config["region"]},
            "image": None, "build": None, "resources": {}, "policies": [],
            "inverse": "Remove only bindings and resources marked created_by_this_run; inspect consumers before deleting new Job/service accounts/custom role.",
        }
        if evidence_path.exists():
            try:
                previous = json.loads(evidence_path.read_text())
            except (OSError, json.JSONDecodeError) as error:
                raise ProvisionError("existing provisioning evidence is unreadable; preserve and inspect it before retry") from error
            if previous.get("schema") not in (fresh["schema"], "lwc-344-exportjob-dev-provision-v1") or previous.get("target") != fresh["target"]:
                raise ProvisionError("existing provisioning evidence belongs to a different contract or target")
            requested_sha = os.getenv("SOURCE_SHA", "")
            previous_sha = previous.get("source", {}).get("sha", "")
            if requested_sha and previous_sha and requested_sha != previous_sha:
                raise ProvisionError("existing provisioning evidence belongs to a different source SHA")
            fresh = previous
            if fresh.get("schema") == "lwc-344-exportjob-dev-provision-v1":
                # Legacy evidence did not retain a provider operation identity; it can never authorize image reuse.
                fresh["build"] = {"status": "unknown", "identity": None, "reason": "legacy_evidence_has_no_build_identity"}
                fresh["schema"] = "lwc-344-exportjob-dev-provision-v2"
            fresh["source"] = {"ref": os.getenv("SOURCE_REF", fresh.get("source", {}).get("ref", "")),
                               "sha": os.getenv("SOURCE_SHA", fresh.get("source", {}).get("sha", ""))}
        self.evidence = fresh

    def save(self) -> None:
        self.evidence_path.parent.mkdir(parents=True, exist_ok=True)
        temp = self.evidence_path.with_suffix(".tmp")
        temp.write_text(json.dumps(self.evidence, indent=2, sort_keys=True) + "\n")
        temp.replace(self.evidence_path)

    def gcloud(self, *args: str, allow_missing: bool = False) -> str | None:
        result = self.run(["gcloud", *args])
        if result.returncode:
            text = result.stderr + result.stdout
            if allow_missing and _is_absent_describe(args, text):
                return None
            diagnostic = _redacted_diagnostic(text) or "no provider diagnostic"
            record = {"operation": "gcloud " + " ".join(args[:3]), "exit_code": result.returncode,
                      "diagnostic": diagnostic}
            self.evidence["provider_failure"] = record
            self.save()
            raise ProvisionError(f"gcloud {args[0]} failed ({result.returncode}): {diagnostic}")
        return result.stdout

    def build_image(self, sha: str) -> str:
        tag = f"{self.c['artifact_registry']}/{IMAGE_REPO}:{sha}"
        source = self.evidence.get("source", {})
        if not source.get("sha"):
            source["sha"] = sha
            self.evidence["source"] = source
            self.save()
        build = self.evidence.get("build")
        if build is not None:
            if source.get("sha") != sha:
                raise ProvisionError("prior build evidence source SHA differs; refusing image reuse or rebuild")
            if (build.get("status") not in {"verified", "submitted", "unknown"} or
                    not BUILD_ID_RE.fullmatch(str(build.get("id", "")))):
                raise ProvisionError("prior build attempt has no proven operation identity; inspect evidence and reconcile before retry")
            if build.get("source_sha") != sha or build.get("image_tag") != tag:
                raise ProvisionError("prior build provenance differs; refusing image reuse or rebuild")
            if build.get("reason") in {"provider_build_identity_or_source_mismatch", "successful_build_has_no_unique_image_digest",
                                        "registry_digest_does_not_match_build_result"}:
                raise ProvisionError("prior provider read-back mismatched; refusing image reuse or rebuild")
            return self._verify_build(build, tag, sha, poll=build.get("status") != "verified")

        self.evidence["build"] = {"status": "submitting", "id": None, "project": self.c["project"],
                                   "region": "global", "source_sha": sha, "source_ref": source.get("ref", ""),
                                   "image_tag": tag, "cli_exit_code": None}
        self.evidence["image"] = {"tag": tag, "status": "pending"}
        self.save()
        command = ["gcloud", "builds", "submit", "apps/bff", "--project", self.c["project"], "--region", "global",
                   "--config", "apps/bff/cloudbuild-exportjob.yaml", "--substitutions", f"_IMAGE={tag},_SOURCE_SHA={sha}",
                   "--async", "--format=value(id)", "--quiet"]
        result = self.run(command)
        build_id = _build_id_from(result)
        self.evidence["build"].update(cli_exit_code=result.returncode)
        if result.returncode != 0:
            self.evidence["build"]["diagnostic"] = _redacted_diagnostic(result.stderr or result.stdout)
        if build_id is None:
            self.evidence["build"].update(status="unknown", reason="submit_returned_no_build_identity")
            self.evidence["image"]["status"] = "unknown"
            self.save()
            raise ProvisionError("Cloud Build submission identity is unknown; refusing an automatic retry")
        self.evidence["build"].update(id=build_id, status="submitted")
        self.save()
        return self._verify_build(self.evidence["build"], tag, sha, poll=True)

    def _verify_build(self, build: dict, tag: str, sha: str, *, poll: bool = False) -> str:
        deadline = time.monotonic() + (600 if poll else 0)
        while True:
            result = self.run(["gcloud", "builds", "describe", build["id"], "--project", self.c["project"],
                               "--region", "global", "--format=json", "--quiet"])
            if result.returncode != 0:
                build.update(status="unknown", reason="provider_status_readback_failed",
                             diagnostic=_redacted_diagnostic(result.stderr or result.stdout))
                self.save()
                raise ProvisionError("Cloud Build status is unknown; inspect the recorded build identity before retry")
            try:
                observed = json.loads(result.stdout)
            except json.JSONDecodeError as error:
                build.update(status="unknown", diagnostic="provider_build_readback_invalid")
                self.save()
                raise ProvisionError("Cloud Build status read-back was invalid") from error
            if (observed.get("id") != build["id"] or observed.get("projectId") != self.c["project"] or
                    observed.get("substitutions", {}).get("_IMAGE") != tag or
                    observed.get("substitutions", {}).get("_SOURCE_SHA") != sha):
                build.update(status="unknown", reason="provider_build_identity_or_source_mismatch")
                self.save()
                raise ProvisionError("Cloud Build identity or source provenance differs; refusing image reuse")
            provider_status = observed.get("status")
            build.update(provider_status=provider_status, create_time=observed.get("createTime"),
                         source_provenance=observed.get("sourceProvenance"),
                         substitutions={key: observed.get("substitutions", {}).get(key) for key in ("_IMAGE", "_SOURCE_SHA")})
            self.save()
            if provider_status == "SUCCESS":
                images = observed.get("results", {}).get("images", [])
                matching = [item for item in images if item.get("name") == tag and re.fullmatch(r"sha256:[0-9a-f]{64}", item.get("digest", ""))]
                if len(matching) != 1:
                    build.update(status="unknown", reason="successful_build_has_no_unique_image_digest")
                    self.save()
                    raise ProvisionError("successful Cloud Build did not provide one matching immutable image digest")
                image = f"{self.c['artifact_registry']}/{IMAGE_REPO}@{matching[0]['digest']}"
                digest = self.gcloud("artifacts", "docker", "images", "describe", tag, "--project", self.c["project"],
                                     "--format=value(image_summary.digest)", "--quiet")
                if (digest or "").strip() != matching[0]["digest"]:
                    build.update(status="unknown", reason="registry_digest_does_not_match_build_result")
                    self.save()
                    raise ProvisionError("Artifact Registry digest does not match the Cloud Build result")
                build.update(status="verified", image_digest=matching[0]["digest"])
                self.evidence["image"] = {"tag": tag, "status": "verified", "reference": image}
                self.save()
                return image
            if provider_status in {"FAILURE", "INTERNAL_ERROR", "TIMEOUT", "CANCELLED", "EXPIRED"}:
                build.update(status="failed")
                self.evidence["image"]["status"] = "failed"
                self.save()
                raise ProvisionError(f"Cloud Build reached terminal status {provider_status}; refusing an automatic retry")
            if not poll or time.monotonic() >= deadline:
                build.update(status="unknown", reason="build_status_not_terminal")
                self.evidence["image"]["status"] = "unknown"
                self.save()
                raise ProvisionError("Cloud Build has no verified terminal status; refusing an automatic retry")
            time.sleep(5)

    def resource_attempt(self, key: str, kind: str, identity: dict, *, create_result: str | None = None,
                         created: bool | None = None, config: dict | None = None,
                         first_observed: str = "absent") -> dict:
        new = key not in self.evidence["resources"]
        entry = self.evidence["resources"].setdefault(key, {
            "kind": kind, "first_observed": first_observed, "creation_status": "pending",
            "created_by_this_run": None, "inverse": "delete only after confirming no consumers",
        })
        if create_result == "pending" and not new:
            raise ProvisionError(f"prior creation evidence exists for {key}; inspect the previous read-back before retry")
        if config is not None:
            entry.setdefault("config", config)
        if create_result is not None:
            old_status = entry.get("creation_status")
            if new or (old_status == "pending" and create_result in ("accepted", "unknown")) or (
                    old_status == "accepted" and create_result == "verified"):
                entry["creation_status"] = create_result
            elif create_result == "verified" and old_status in ("pending", "unknown", "preexisting"):
                entry["readback_status"] = "verified"
        if ((new and created is not None) or (created is True and entry.get("created_by_this_run") is None)):
            entry["created_by_this_run"] = created
        entry["readback_identity"] = identity
        self.save()
        return entry

    def ensure_service_account(self, email: str, name: str, display_name: str) -> None:
        project = self.c["project"]
        description = self.gcloud("iam", "service-accounts", "describe", email, "--project", project, "--format=json", "--quiet", allow_missing=True)
        created = False
        if description is not None:
            observed = json.loads(description)
            self.resource_attempt(email, "serviceAccount", {"email": observed.get("email"), "name": observed.get("name")},
                                  create_result="preexisting", created=False, first_observed="present")
        if description is None:
            self.resource_attempt(email, "serviceAccount", {"email": email}, create_result="pending")
            result = self.run(["gcloud", "iam", "service-accounts", "create", name, "--project", project,
                               "--display-name", display_name, "--description", "LWC-344 DEV Export", "--quiet"])
            self.resource_attempt(email, "serviceAccount", {"email": email},
                                  create_result="accepted" if result.returncode == 0 else "unknown",
                                  created=True if result.returncode == 0 else None)
            if result.returncode:
                # A timed-out create may have been accepted; exact read-back is the recovery check.
                description = self.gcloud("iam", "service-accounts", "describe", email, "--project", project, "--format=json", "--quiet", allow_missing=True)
                if description is None:
                    raise ProvisionError("service account create did not converge; rerun after reviewing the workflow log")
            else:
                created = True
                description = self.gcloud("iam", "service-accounts", "describe", email, "--project", project, "--format=json", "--quiet")
        actual = json.loads(description)
        self.resource_attempt(email, "serviceAccount", {"email": actual.get("email"), "name": actual.get("name")},
                              created=created)
        if actual.get("email") != email or actual.get("name", "").split("/")[-1] != email:
            raise ProvisionError(f"existing service account {email} is partial or mismatched")
        self.resource_attempt(email, "serviceAccount", {"email": actual["email"], "name": actual["name"]},
                              create_result="verified", created=created)

    def verify_service_account(self, email: str) -> None:
        raw = self.gcloud("iam", "service-accounts", "describe", email, "--project", self.c["project"],
                          "--format=json", "--quiet")
        observed = json.loads(raw or "{}")
        if observed.get("email") != email or observed.get("name", "").split("/")[-1] != email:
            raise ProvisionError(f"required service account {email} is absent or mismatched")
        self.resource_attempt(email, "serviceAccount", {"email": observed["email"], "name": observed["name"]},
                              create_result="verified", created=False, first_observed="present")

    def image_and_job_config(self, image: str) -> dict:
        c = self.c
        return {"image": image, "service_account": c["runtime_service_account"], "env": {
            "GCP_PROJECT": c["project"], "BUCKET": c["bucket"], "FIRESTORE_DATABASE_ID": c["database"],
            "EXPORT_SIGNING_SERVICE_ACCOUNT": c["signing_service_account"]},
            "timeout": c["job_timeout"], "max_retries": c["max_retries"],
            "parallelism": c["parallelism"], "tasks": c["tasks"]}

    def job_matches(self, raw: str, expected: dict) -> bool:
        job = json.loads(raw)
        if job.get("apiVersion") == "run.googleapis.com/v1" or "spec" in job:
            execution = job.get("spec", {}).get("template", {}).get("spec", {})
            task = execution.get("template", {}).get("spec", {})
            account_key, timeout_key = "serviceAccountName", "timeoutSeconds"
        else:
            execution = job.get("template", {})
            task = execution.get("template", {})
            account_key, timeout_key = "serviceAccount", "timeout"
        containers = task.get("containers", [])
        env = {entry.get("name"): entry.get("value") for entry in (containers[0].get("env", []) if len(containers) == 1 else [])}
        timeout = task.get(timeout_key)
        timeout_matches = _timeout_matches(timeout, expected["timeout"])
        return (len(containers) == 1 and containers[0].get("image") == expected["image"] and
                task.get(account_key) == expected["service_account"] and env == expected["env"] and
                timeout_matches and task.get("maxRetries") == expected["max_retries"] and
                execution.get("parallelism") == expected["parallelism"] and execution.get("taskCount") == expected["tasks"])

    def continue_existing_job(self, prior: dict, *, run_id: str, owner_source_sha: str, sha: str) -> None:
        """Adopt only a matching Job backed by the exact previously verified Cloud Build operation."""
        c = self.c
        if (self.evidence.get("source", {}).get("sha") != sha or
                self.evidence.get("source", {}).get("ref") != "refs/heads/develop"):
            raise ProvisionError("continuation source differs from the exact current develop workflow SHA")
        build = prior.get("build") or {}
        old_source = prior.get("source") or {}
        image = prior.get("image") or {}
        resource = (prior.get("resources") or {}).get(c["job"], {})
        image_tag = f"{c['artifact_registry']}/{IMAGE_REPO}:{old_source.get('sha', '')}"
        digest = build.get("image_digest", "")
        expected_ref = f"{c['artifact_registry']}/{IMAGE_REPO}@{digest}"
        if (not run_id.isdecimal() or prior.get("target") != {"environment": "development", "project": c["project"], "region": c["region"]} or
                old_source.get("ref") != "refs/heads/develop" or not re.fullmatch(r"[0-9a-f]{40}", old_source.get("sha", "")) or
                build.get("status") != "verified" or build.get("source_ref") != old_source.get("ref") or
                build.get("source_sha") != old_source.get("sha") or build.get("project") != c["project"] or
                build.get("region") != "global" or build.get("image_tag") != image_tag or
                not BUILD_ID_RE.fullmatch(str(build.get("id", ""))) or
                not re.fullmatch(r"sha256:[0-9a-f]{64}", digest) or image.get("status") != "verified" or
                image.get("reference") != expected_ref or resource.get("kind") != "cloudRunJob" or
                resource.get("created_by_this_run") is not True or
                (resource.get("readback_identity") or {}).get("name") != c["job"] or
                not (resource.get("readback_identity") or {}).get("uid")):
            raise ProvisionError("continuation evidence does not prove the recorded DEV build and created Job")
        if (owner_source_sha != "466b54a358c5d6a9274d9c085078fc7dd2a1b938" or
                not re.fullmatch(r"[0-9a-f]{40}", owner_source_sha)):
            raise ProvisionError("continuation requires the original LWC-344 owner source SHA")
        source_contract = subprocess.run(["git", "show", f"{owner_source_sha}:deploy/provision/exportjob-dev.json"],
                                         cwd=ROOT, text=True, capture_output=True)
        source_code = subprocess.run(["git", "show", f"{owner_source_sha}:deploy/provision/exportjob_dev.py"],
                                     cwd=ROOT, text=True, capture_output=True)
        current_code = Path(__file__).read_text()
        if (source_contract.returncode or json.loads(source_contract.stdout) != load_contract() or source_code.returncode or
                not _timeout_source_repair_is_approved(source_code.stdout, current_code) or
                not _timeout_normalizer_is_approved(current_code) or
                _resource_iam_source_signature(source_code.stdout) != _resource_iam_source_signature(current_code)):
            raise ProvisionError("DEV runtime or IAM source differs from the original owner contract")
        if os.getenv("WIF_SERVICE_ACCOUNT", "") != DEPLOYER.removeprefix("serviceAccount:"):
            raise ProvisionError("workflow identity differs from the reviewed DEV deployer service account")

        # Keep the provider's original source SHA in this build record; only the enclosing workflow SHA is new.
        self.evidence["build"] = copy.deepcopy(build)
        verified_image = self._verify_build(self.evidence["build"], image_tag, old_source["sha"])
        if verified_image != expected_ref:
            raise ProvisionError("recorded build digest changed during provider read-back")
        raw = self.gcloud("run", "jobs", "describe", c["job"], "--project", c["project"], "--region", c["region"],
                          "--format=json", "--quiet")
        actual = json.loads(raw or "{}")
        metadata = actual.get("metadata", {})
        desired = self.image_and_job_config(expected_ref)
        status = actual.get("status", {})
        if actual.get("apiVersion") == "run.googleapis.com/v1" or "spec" in actual:
            execution = actual.get("spec", {}).get("template", {}).get("spec", {})
            task = execution.get("template", {}).get("spec", {})
        else:
            execution = actual.get("template", {})
            task = execution.get("template", {})
        containers = task.get("containers", [])
        ready = any(condition.get("type") == "Ready" and condition.get("status") == "True"
                    for condition in status.get("conditions", []))
        if (metadata.get("name") != c["job"] or metadata.get("uid") != resource["readback_identity"]["uid"] or
                status.get("observedGeneration") != metadata.get("generation") or not ready or
                task.get("volumes", []) or len(containers) != 1 or containers[0].get("args") not in (None, []) or
                containers[0].get("volumeMounts", []) or not self.job_matches(raw or "{}", desired)):
            raise ProvisionError("live DEV Job identity or runtime differs from the recorded successful build")
        self.resource_attempt(c["job"], "cloudRunJob", {"name": c["job"], "uid": metadata["uid"]},
                              create_result="preexisting", created=False, config=desired, first_observed="present")
        self.resource_attempt(c["job"], "cloudRunJob", {"name": c["job"], "uid": metadata["uid"]},
                              create_result="verified", created=False, config=desired)
        self.evidence["image"] = {"tag": image_tag, "status": "verified", "reference": expected_ref}
        self.evidence["continuation"] = {"prior_run_id": run_id, "prior_source": copy.deepcopy(old_source),
                                         "owner_source_sha": owner_source_sha, "reason": "adopted_existing_job_after_readback_format_repair"}
        self.apply_job_iam()
        self.evidence["result"] = "workflow_deployed_and_read_back"
        self.save()

    def ensure_job(self, image: str) -> None:
        c = self.c
        args = ("run", "jobs", "describe", c["job"], "--project", c["project"], "--region", c["region"], "--format=json", "--quiet")
        raw = self.gcloud(*args, allow_missing=True)
        created = False
        desired = self.image_and_job_config(image)
        if raw is not None:
            observed = json.loads(raw)
            metadata = observed.get("metadata", {})
            self.resource_attempt(c["job"], "cloudRunJob", {"name": metadata.get("name", c["job"]),
                                                               "uid": metadata.get("uid")},
                                  create_result="preexisting", created=False, config=desired, first_observed="present")
        if raw is None:
            self.resource_attempt(c["job"], "cloudRunJob", {"name": c["job"]}, create_result="pending", config=desired)
            command = ["gcloud", "run", "jobs", "create", c["job"], "--project", c["project"], "--region", c["region"],
                       "--image", image, "--service-account", c["runtime_service_account"], "--task-timeout", c["job_timeout"],
                       "--max-retries", str(c["max_retries"]), "--parallelism", str(c["parallelism"]), "--tasks", str(c["tasks"]),
                       "--set-env-vars", "^|^" + "|".join(f"{key}={value}" for key, value in desired["env"].items()), "--quiet"]
            result = self.run(command)
            self.resource_attempt(c["job"], "cloudRunJob", {"name": c["job"]},
                                  create_result="accepted" if result.returncode == 0 else "unknown",
                                  created=True if result.returncode == 0 else None, config=desired)
            if result.returncode:
                raw = self.gcloud(*args, allow_missing=True)
                if raw is None:
                    raise ProvisionError("Job create did not converge; rerun after reviewing the workflow log")
            else:
                raw, created = self.gcloud(*args), True
        actual = json.loads(raw or "{}")
        metadata = actual.get("metadata", {})
        self.resource_attempt(c["job"], "cloudRunJob", {"name": metadata.get("name", c["job"]),
                                                           "uid": metadata.get("uid")}, config=desired)
        if not self.job_matches(raw or "{}", desired):
            self.resource_attempt(c["job"], "cloudRunJob", {"name": metadata.get("name", c["job"]),
                                                               "uid": metadata.get("uid"), "readback": "mismatched"}, config=desired)
            raise ProvisionError("existing Export Job is partial or differs from the reviewed DEV runtime contract")
        self.resource_attempt(c["job"], "cloudRunJob", {"name": c["job"], "config": desired},
                              create_result="verified", created=created, config=desired)

    def ensure_role(self, role_id: str, title: str, description: str, permissions: list[str]) -> str:
        c = self.c
        existing = self.gcloud("iam", "roles", "describe", role_id, "--project", c["project"], "--format=json", "--quiet", allow_missing=True)
        created = False
        name = f"projects/{c['project']}/roles/{role_id}"
        if existing is not None:
            observed = json.loads(existing)
            self.resource_attempt(name, "customRole", {"name": observed.get("name", role_id),
                                                         "included_permissions": observed.get("includedPermissions", []),
                                                         "stage": observed.get("stage")},
                                  create_result="preexisting", created=False, first_observed="present")
        if existing is None:
            self.resource_attempt(name, "customRole", {"role_id": role_id}, create_result="pending")
            result = self.run(["gcloud", "iam", "roles", "create", role_id, "--project", c["project"],
                               "--title", title, "--description", description,
                               "--permissions", ",".join(permissions), "--stage", "GA", "--quiet"])
            self.resource_attempt(name, "customRole", {"role_id": role_id},
                                  create_result="accepted" if result.returncode == 0 else "unknown",
                                  created=True if result.returncode == 0 else None)
            if result.returncode:
                existing = self.gcloud("iam", "roles", "describe", role_id, "--project", c["project"], "--format=json", "--quiet", allow_missing=True)
                if existing is None:
                    raise ProvisionError("list-only custom role create did not converge")
            else:
                existing, created = self.gcloud("iam", "roles", "describe", role_id, "--project", c["project"], "--format=json", "--quiet"), True
        role = json.loads(existing or "{}")
        self.resource_attempt(name, "customRole", {"name": role.get("name", role_id),
                                                     "included_permissions": role.get("includedPermissions", []),
                                                     "stage": role.get("stage")}, created=created)
        if sorted(role.get("includedPermissions", [])) != sorted(permissions) or role.get("stage") != "GA":
            raise ProvisionError(f"existing custom role {role_id} is partial or has extra permissions")
        name = f"projects/{c['project']}/roles/{role_id}"
        self.resource_attempt(name, "customRole", {"role_id": role_id, "included_permissions": role.get("includedPermissions", []),
                                                     "stage": role.get("stage")},
                              create_result="verified", created=created)
        return name

    def ensure_custom_role(self, role_key: str) -> str:
        role_id = {"objectLister": "lwcExportObjectLister", "sourceReader": "lwcExportSourceReader",
                   "archiveWriter": "lwcExportArchiveWriter", "blobSigner": "lwcExportBlobSigner",
                   "readyArchiveReader": "lwcExportReadyArchiveReader"}[role_key]
        title = {"objectLister": "LWC DEV Export Object List", "sourceReader": "LWC DEV Export Source Reader",
                 "archiveWriter": "LWC DEV Export Archive Writer", "blobSigner": "LWC DEV Export Blob Signer",
                 "readyArchiveReader": "LWC DEV Export Ready Archive Reader"}[role_key]
        return self.ensure_role(role_id, title, f"LWC-344 DEV Export {role_key}", self.c["storage_roles"][role_key])

    def ensure_verifier_roles(self) -> dict[str, str]:
        roles = {}
        for key, (role_id, title, permissions) in VERIFIER_ROLES.items():
            description = "LWC-344 DEV Export readback only; grants no IAM mutation authority"
            if key == "projectPolicyReadback":
                description += "; read access exposes all project IAM metadata, not secrets"
            roles[key] = self.ensure_role(role_id, title, description, permissions)
        return roles

    def verify_custom_role(self, role_key: str) -> str:
        c = self.c
        role_id = {"objectLister": "lwcExportObjectLister", "sourceReader": "lwcExportSourceReader",
                   "archiveWriter": "lwcExportArchiveWriter", "blobSigner": "lwcExportBlobSigner",
                   "readyArchiveReader": "lwcExportReadyArchiveReader"}[role_key]
        expected = c["storage_roles"][role_key]
        role = json.loads(self.gcloud("iam", "roles", "describe", role_id, "--project", c["project"],
                                      "--format=json", "--quiet") or "{}")
        name = f"projects/{c['project']}/roles/{role_id}"
        if (role.get("name") != name or role.get("stage") != "GA" or
                sorted(role.get("includedPermissions", [])) != sorted(expected)):
            raise ProvisionError(f"required custom role {role_id} is absent or differs from the reviewed permission set")
        self.resource_attempt(name, "customRole", {"name": role["name"],
            "included_permissions": role["includedPermissions"], "stage": role["stage"]},
            create_result="verified", created=False, first_observed="present")
        return name

    def verify_verifier_roles(self) -> dict[str, str]:
        roles = {}
        for key, (role_id, _title, permissions) in VERIFIER_ROLES.items():
            name = f"projects/{self.c['project']}/roles/{role_id}"
            role = json.loads(self.gcloud("iam", "roles", "describe", role_id, "--project", self.c["project"],
                                          "--format=json", "--quiet") or "{}")
            if (role.get("name") != name or role.get("stage") != "GA" or
                    role.get("includedPermissions") != permissions):
                raise ProvisionError(f"required verifier role {role_id} is absent or differs from its exact read permission")
            self.resource_attempt(name, "customRole", {"name": name, "included_permissions": permissions,
                "stage": "GA"}, create_result="verified", created=False, first_observed="present")
            roles[key] = name
        return roles

    def bucket_ready(self) -> None:
        raw = self.gcloud("storage", "buckets", "describe", f"gs://{self.c['bucket']}", "--format=json", "--quiet")
        bucket = json.loads(raw or "{}")
        iam = bucket.get("iam_configuration")
        normalized = bucket.get("uniform_bucket_level_access") is True or (
            isinstance(iam, dict) and iam.get("uniform_bucket_level_access", {}).get("enabled") is True)
        if not normalized:
            raise ProvisionError("DEV bucket must already use uniform bucket-level access; provisioning will not change bucket configuration")

    def policy(self, target: str, get_args: list[str], set_args: list[str], desired: dict) -> None:
        entry = {"target": target, "binding": desired, "attempts": [], "status": "prepared"}
        self.evidence["policies"].append(entry)
        self.save()
        for attempt in range(MAX_ETAG_ATTEMPTS):
            before_text = self.gcloud(*get_args)
            before = json.loads(before_text or "{}")
            entry["attempts"].append({"before_etag": before.get("etag"), "before_policy": before})
            self.save()
            policy = copy.deepcopy(before)
            if desired.get("condition"):
                policy["version"] = max(policy.get("version", 1), 3)
            bindings = policy.setdefault("bindings", [])
            exact = next((binding for binding in bindings if binding.get("role") == desired["role"] and
                          binding.get("condition") == desired.get("condition")), None)
            if exact and desired["member"] in exact.get("members", []):
                entry.update(status="existing_verified", after_etag=before.get("etag"), after_policy=before,
                             added_by_this_run=False)
                self.save()
                return
            conflict = next((binding for binding in bindings if binding.get("role") == desired["role"] and
                             desired["member"] in binding.get("members", []) and
                             binding.get("condition") != desired.get("condition")), None)
            if conflict:
                entry.update(status="existing_member_has_different_condition", after_etag=before.get("etag"),
                             after_policy=before, conflicting_binding=conflict, added_by_this_run=False)
                self.save()
                raise ProvisionError(f"IAM policy for {target} already grants the member with a different condition")
            if exact:
                exact.setdefault("members", []).append(desired["member"])
            else:
                binding = {"role": desired["role"], "members": [desired["member"]]}
                if desired.get("condition"):
                    binding["condition"] = desired["condition"]
                bindings.append(binding)
            if not before.get("etag"):
                entry["status"] = "missing_etag"
                self.save()
                raise ProvisionError(f"IAM policy for {target} has no etag; no update was attempted")
            with tempfile.TemporaryDirectory() as temp_dir:
                path = Path(temp_dir) / "policy.json"
                path.write_text(json.dumps(policy))
                result = self.run(["gcloud", *set_args, str(path), "--quiet"])
            if result.returncode:
                if _etag_conflict(result) and attempt + 1 < MAX_ETAG_ATTEMPTS:
                    entry["status"] = "etag_conflict_rereading"
                    self.save()
                    continue
                entry["status"] = "mutation_failed_or_ambiguous"
                self.save()
                raise ProvisionError(f"IAM policy update for {target} failed; rerun only after read-back")
            entry["status"] = "update_accepted_readback_pending"
            self.save()
            try:
                after = json.loads(self.gcloud(*get_args) or "{}")
            except (ProvisionError, ValueError, json.JSONDecodeError):
                entry["status"] = "update_accepted_readback_unknown"
                self.save()
                raise
            if not after.get("etag") or not self.policy_has(after, desired) or not self.policy_preserves(before, after):
                entry.update(status="readback_missing_binding", after_etag=after.get("etag"), after_policy=after)
                self.save()
                raise ProvisionError(f"IAM read-back for {target} is missing the requested binding or changed an existing binding")
            entry.update(status="verified_addition", after_etag=after.get("etag"), after_policy=after,
                         added_by_this_run=True)
            self.save()
            return
        entry["status"] = "etag_churn_limit_reached"
        self.save()
        raise ProvisionError(f"IAM policy for {target} kept changing; no further retry was attempted")

    @staticmethod
    def policy_has(policy: dict, desired: dict) -> bool:
        return any(binding.get("role") == desired["role"] and binding.get("condition") == desired.get("condition") and
                   desired["member"] in binding.get("members", []) for binding in policy.get("bindings", []))

    @staticmethod
    def policy_preserves(before: dict, after: dict) -> bool:
        def bindings(policy: dict) -> dict[tuple[str, str], set[str]]:
            merged: dict[tuple[str, str], set[str]] = {}
            for item in policy.get("bindings", []):
                key = (item.get("role", ""), json.dumps(item.get("condition"), sort_keys=True))
                merged.setdefault(key, set()).update(item.get("members", []))
            return merged
        old, new = bindings(before), bindings(after)
        return all(members <= new.get(key, set()) for key, members in old.items())

    def apply_iam(self, list_role: str, source_role: str, archive_role: str, signer_role: str,
                  ready_archive_role: str) -> None:
        c = self.c
        project = c["project"]
        runtime = "serviceAccount:" + c["runtime_service_account"]
        bff = "serviceAccount:" + c["bff_service_account"]
        datastore = {"role": "roles/datastore.user", "member": runtime, "condition": {
            "title": "lwc344-export-worker-dev-firestore", "description": "Limit Export worker to the DEV Firestore database",
            "expression": DB_CONDITION}}
        self.policy("project:" + project, ["projects", "get-iam-policy", project, "--format=json", "--quiet"],
                    ["projects", "set-iam-policy", project], datastore)
        storage = [
            {"role": list_role, "member": runtime},
            {"role": source_role, "member": runtime, "condition": {
                "title": "lwc344-export-source-objects", "description": "Read Export source objects only",
                "expression": STORAGE_USERS}},
            {"role": archive_role, "member": runtime, "condition": {
                "title": "lwc344-export-archive-objects", "description": "Manage Export temporary and ready archives only",
                "expression": STORAGE_EXPORTS}},
        ]
        for binding in storage:
            self.policy("bucket:" + c["bucket"], ["storage", "buckets", "get-iam-policy", f"gs://{c['bucket']}", "--format=json", "--quiet"],
                        ["storage", "buckets", "set-iam-policy", f"gs://{c['bucket']}"], binding)
        ready_archive = ready_archive_binding(ready_archive_role, c["signing_service_account"])
        self.policy("bucket:" + c["bucket"], ["storage", "buckets", "get-iam-policy", f"gs://{c['bucket']}", "--format=json", "--quiet"],
                    ["storage", "buckets", "set-iam-policy", f"gs://{c['bucket']}"], ready_archive)
        signer_binding = {"role": signer_role, "member": bff}
        signer = c["signing_service_account"]
        self.policy("serviceAccount:" + signer,
                    ["iam", "service-accounts", "get-iam-policy", signer, "--project", project, "--format=json", "--quiet"],
                    ["iam", "service-accounts", "set-iam-policy", signer, "--project", project], signer_binding)

    def apply_verifier_iam(self, roles: dict[str, str]) -> None:
        project = self.c["project"]
        for role_key in ("roleReadback", "projectPolicyReadback"):
            desired = {"role": roles[role_key], "member": DEPLOYER}
            self.policy("project:" + project, ["projects", "get-iam-policy", project, "--format=json", "--quiet"],
                        ["projects", "set-iam-policy", project], desired)
        signer = self.c["signing_service_account"]
        desired = {"role": roles["signerPolicyReadback"], "member": DEPLOYER}
        self.policy("serviceAccount:" + signer,
                    ["iam", "service-accounts", "get-iam-policy", signer, "--project", project, "--format=json", "--quiet"],
                    ["iam", "service-accounts", "set-iam-policy", signer, "--project", project], desired)

    def apply_job_iam(self) -> None:
        c = self.c
        binding = {"role": "roles/run.jobsExecutorWithOverrides",
                   "member": "serviceAccount:" + c["bff_service_account"]}
        self.policy("job:" + c["job"],
                    ["run", "jobs", "get-iam-policy", c["job"], "--project", c["project"], "--region", c["region"], "--format=json", "--quiet"],
                    ["run", "jobs", "set-iam-policy", c["job"], "--project", c["project"], "--region", c["region"]], binding)

    def verify_policy(self, target: str, get_args: list[str], desired: dict) -> None:
        policy = json.loads(self.gcloud(*get_args) or "{}")
        if not policy.get("etag") or not self.policy_has(policy, desired):
            raise ProvisionError(f"required IAM binding is absent or mismatched on {target}")
        entry = {"target": target, "binding": desired, "status": "existing_verified",
                 "before_etag": policy["etag"], "added_by_this_run": False}
        self.evidence["policies"].append(entry)
        self.save()

    def remove_policy_member(self, target: str, get_args: list[str], set_args: list[str], desired: dict,
                             entry: dict) -> None:
        for attempt in range(MAX_ETAG_ATTEMPTS):
            before = json.loads(self.gcloud(*get_args) or "{}")
            entry.setdefault("cleanup_attempts", []).append({"before_etag": before.get("etag"), "before_policy": before})
            self.save()
            bindings = before.get("bindings", [])
            exact = next((binding for binding in bindings if binding.get("role") == desired["role"] and
                          binding.get("condition") == desired.get("condition")), None)
            conflict = next((binding for binding in bindings if binding.get("role") == desired["role"] and
                             desired["member"] in binding.get("members", []) and
                             binding.get("condition") != desired.get("condition")), None)
            if conflict:
                raise ProvisionError(f"verifier binding for {target} changed condition; cleanup stopped")
            if exact is None or desired["member"] not in exact.get("members", []):
                entry.update(cleanup_status="already_absent", removed_by_cleanup=False)
                self.save()
                return
            if not before.get("etag"):
                raise ProvisionError(f"IAM policy for {target} has no etag; cleanup was not attempted")
            updated = copy.deepcopy(before)
            expected_after = copy.deepcopy(before)
            selected = next(binding for binding in updated.get("bindings", []) if binding.get("role") == desired["role"] and
                            binding.get("condition") == desired.get("condition"))
            expected_selected = next(binding for binding in expected_after.get("bindings", []) if binding.get("role") == desired["role"] and
                                     binding.get("condition") == desired.get("condition"))
            selected["members"].remove(desired["member"])
            expected_selected["members"].remove(desired["member"])
            if not selected["members"]:
                updated["bindings"].remove(selected)
                expected_after["bindings"].remove(expected_selected)
            with tempfile.TemporaryDirectory() as temp_dir:
                path = Path(temp_dir) / "policy.json"
                path.write_text(json.dumps(updated))
                result = self.run(["gcloud", *set_args, str(path), "--quiet"])
            if result.returncode:
                if _etag_conflict(result) and attempt + 1 < MAX_ETAG_ATTEMPTS:
                    continue
                raise ProvisionError(f"IAM verifier cleanup for {target} failed; inspect policy read-back before retry")
            after = json.loads(self.gcloud(*get_args) or "{}")
            if (not after.get("etag") or self.policy_has(after, desired) or
                    not self.policy_preserves(expected_after, after)):
                raise ProvisionError(f"IAM verifier cleanup read-back for {target} failed preservation checks")
            entry.update(cleanup_status="verified_removed", cleanup_after_etag=after["etag"],
                         cleanup_after_policy=after, removed_by_cleanup=True)
            self.save()
            return
        raise ProvisionError(f"IAM policy for {target} kept changing; cleanup stopped after fresh etag retries")

    def cleanup_verifier_grants(self, workflow_evidence_path: Path,
                                owner_source_repair_evidence_path: Path | None = None) -> None:
        try:
            workflow = json.loads(workflow_evidence_path.read_text())
        except (OSError, json.JSONDecodeError) as error:
            raise ProvisionError("workflow verification evidence is unreadable; verifier cleanup stopped") from error
        owner = self.evidence
        repair = owner_source_repair_evidence_path is not None
        if repair:
            try:
                owner = json.loads(owner_source_repair_evidence_path.read_text())
            except (OSError, json.JSONDecodeError) as error:
                raise ProvisionError("original owner evidence is unreadable; verifier cleanup stopped") from error
        owner_sha = owner.get("source", {}).get("sha", "")
        workflow_sha = workflow.get("source", {}).get("sha", "")
        current_sha = self.evidence.get("source", {}).get("sha", "")
        same_source = owner_sha == workflow_sha
        if (owner.get("result") != "owner_bootstrap_applied_and_read_back" or
                (not same_source and not repair) or
                (repair and (not re.fullmatch(r"[0-9a-f]{40}", owner_sha) or owner_sha == workflow_sha)) or
                owner.get("source", {}).get("ref") != "refs/heads/develop" or
                workflow.get("result") != "workflow_deployed_and_read_back" or
                workflow.get("schema") != "lwc-344-exportjob-dev-provision-v2" or
                workflow.get("source", {}).get("ref") != "refs/heads/develop" or
                not re.fullmatch(r"[0-9a-f]{40}", workflow_sha) or
                workflow.get("source", {}).get("sha") != current_sha or
                self.evidence.get("source", {}).get("ref") != "refs/heads/develop" or
                not re.fullmatch(r"[0-9a-f]{40}", current_sha) or
                workflow.get("target") != self.evidence.get("target") or
                owner.get("target") != workflow.get("target") or
                workflow.get("image", {}).get("status") != "verified"):
            raise ProvisionError("cleanup requires matching owner-bootstrap and successful workflow verification evidence")
        if repair:
            self._verify_owner_contract(owner)
        job = workflow.get("resources", {}).get(self.c["job"], {})
        if job.get("creation_status") != "verified" and job.get("readback_status") != "verified":
            raise ProvisionError("cleanup requires verified Cloud Run Job read-back evidence")
        roles = {key: f"projects/{self.c['project']}/roles/{value[0]}" for key, value in VERIFIER_ROLES.items()}
        specs = [
            ("project:" + self.c["project"], roles["roleReadback"], ["projects", "get-iam-policy", self.c["project"], "--format=json", "--quiet"],
             ["projects", "set-iam-policy", self.c["project"]]),
            ("project:" + self.c["project"], roles["projectPolicyReadback"], ["projects", "get-iam-policy", self.c["project"], "--format=json", "--quiet"],
             ["projects", "set-iam-policy", self.c["project"]]),
            ("serviceAccount:" + self.c["signing_service_account"], roles["signerPolicyReadback"],
             ["iam", "service-accounts", "get-iam-policy", self.c["signing_service_account"], "--project", self.c["project"], "--format=json", "--quiet"],
             ["iam", "service-accounts", "set-iam-policy", self.c["signing_service_account"], "--project", self.c["project"]]),
        ]
        cleanup_entries = []
        for target, role, get_args, set_args in specs:
            desired = {"role": role, "member": DEPLOYER}
            if not any(item.get("target") == target and item.get("binding") == desired and
                       item.get("status") == "existing_verified" and item.get("before_etag")
                       for item in workflow.get("policies", [])):
                raise ProvisionError(f"workflow evidence does not verify {role}; verifier cleanup stopped")
            entry = next((item for item in reversed(owner["policies"])
                          if item.get("target") == target and item.get("binding") == desired), None)
            if entry is None:
                raise ProvisionError(f"owner evidence has no exact bootstrap record for {role}")
            if repair:
                journal_entry = next((item for item in reversed(self.evidence["policies"])
                                      if item.get("source_repair_owner_sha") == owner_sha and
                                      item.get("target") == target and item.get("binding") == desired), None)
                if journal_entry is None:
                    journal_entry = copy.deepcopy(entry)
                    journal_entry["source_repair_owner_sha"] = owner_sha
                    journal_entry["source_repair_workflow_sha"] = workflow_sha
                    if not journal_entry.get("inverse"):
                        journal_entry["inverse"] = "Remove only this exact verifier member with a fresh-etag write; preserve and verify all other policy members."
                    self.evidence["policies"].append(journal_entry)
                cleanup_entries.append((target, get_args, set_args, desired, journal_entry))
            else:
                cleanup_entries.append((target, get_args, set_args, desired, entry))
        if repair:
            self.evidence["source_repair"] = {"owner_source_sha": owner_sha, "workflow_source_sha": workflow_sha,
                                               "owner_receipt": "preserved_separate_file"}
            self.save()
        for target, get_args, set_args, desired, entry in cleanup_entries:
            if entry.get("added_by_this_run") is True:
                self.remove_policy_member(target, get_args, set_args, desired, entry)
        self.evidence["verifier_cleanup"] = "complete"
        self.save()

    def _verify_owner_contract(self, owner: dict) -> None:
        """Compare preserved old-SHA owner facts with the current fixed DEV contract."""
        if owner.get("source", {}).get("sha") != "466b54a358c5d6a9274d9c085078fc7dd2a1b938":
            raise ProvisionError("original owner evidence is not from the preserved LWC-344 owner SHA")
        source_contract = subprocess.run(["git", "show", f"{owner['source']['sha']}:deploy/provision/exportjob-dev.json"],
                                         cwd=ROOT, text=True, capture_output=True)
        if source_contract.returncode != 0:
            raise ProvisionError("original owner source is unavailable; cannot validate source-repair contract")
        try:
            old_contract = json.loads(source_contract.stdout)
        except json.JSONDecodeError as error:
            raise ProvisionError("original owner source contract is invalid") from error
        if old_contract != load_contract():
            raise ProvisionError("DEV resource contract changed across source repair; verifier cleanup stopped")
        source_code = subprocess.run(["git", "show", f"{owner['source']['sha']}:deploy/provision/exportjob_dev.py"],
                                     cwd=ROOT, text=True, capture_output=True)
        current_code = Path(__file__).read_text()
        if (source_code.returncode != 0 or not _timeout_source_repair_is_approved(source_code.stdout, current_code) or
                not _timeout_normalizer_is_approved(current_code) or
                _resource_iam_source_signature(source_code.stdout) != _resource_iam_source_signature(current_code)):
            raise ProvisionError("DEV resource or IAM implementation changed across source repair; verifier cleanup stopped")
        expected_roles = {f"projects/{self.c['project']}/roles/{role}": sorted(perms)
                          for role, perms in (("lwcExportObjectLister", ["storage.objects.list"]),
                                              ("lwcExportSourceReader", ["storage.objects.get"]),
                                              ("lwcExportArchiveWriter", ["storage.objects.create", "storage.objects.delete", "storage.objects.get", "storage.objects.update"]),
                                              ("lwcExportBlobSigner", ["iam.serviceAccounts.signBlob"]),
                                              ("lwcExportReadyArchiveReader", ["storage.objects.get"]),
                                              ("lwcExportRoleReadback", ["iam.roles.get"]),
                                              ("lwcExportProjectPolicyReadback", ["resourcemanager.projects.getIamPolicy"]),
                                              ("lwcExportSignerPolicyReadback", ["iam.serviceAccounts.getIamPolicy"]))}
        resources = owner.get("resources", {})
        for email in (self.c["runtime_service_account"], self.c["signing_service_account"]):
            entry = resources.get(email, {})
            if entry.get("creation_status") != "verified" or not isinstance(entry.get("readback_identity"), dict):
                raise ProvisionError("original owner evidence does not verify both DEV service accounts")
        for name, permissions in expected_roles.items():
            entry = resources.get(name, {})
            identity = entry.get("readback_identity", {})
            if (entry.get("creation_status") != "verified" or identity.get("stage") != "GA" or
                    sorted(identity.get("included_permissions", [])) != permissions):
                raise ProvisionError(f"original owner evidence differs from the DEV role contract: {name}")
        policy_bindings = {(item.get("target"), json.dumps(item.get("binding"), sort_keys=True))
                           for item in owner.get("policies", [])
                           if item.get("status") in {"existing_verified", "verified_addition"}}
        required = [
            ("project:" + self.c["project"], {"role": "roles/datastore.user", "member": "serviceAccount:" + self.c["runtime_service_account"],
             "condition": {"title": "lwc344-export-worker-dev-firestore", "description": "Limit Export worker to the DEV Firestore database", "expression": DB_CONDITION}}),
            ("bucket:" + self.c["bucket"], {"role": "projects/llm-wiki-cloud/roles/lwcExportObjectLister", "member": "serviceAccount:" + self.c["runtime_service_account"]}),
            ("bucket:" + self.c["bucket"], {"role": "projects/llm-wiki-cloud/roles/lwcExportSourceReader", "member": "serviceAccount:" + self.c["runtime_service_account"],
             "condition": {"title": "lwc344-export-source-objects", "description": "Read Export source objects only", "expression": STORAGE_USERS}}),
            ("bucket:" + self.c["bucket"], {"role": "projects/llm-wiki-cloud/roles/lwcExportArchiveWriter", "member": "serviceAccount:" + self.c["runtime_service_account"],
             "condition": {"title": "lwc344-export-archive-objects", "description": "Manage Export temporary and ready archives only", "expression": STORAGE_EXPORTS}}),
            ("bucket:" + self.c["bucket"], ready_archive_binding("projects/llm-wiki-cloud/roles/lwcExportReadyArchiveReader", self.c["signing_service_account"])),
            ("serviceAccount:" + self.c["signing_service_account"], {"role": "projects/llm-wiki-cloud/roles/lwcExportBlobSigner", "member": "serviceAccount:" + self.c["bff_service_account"]}),
        ]
        required.extend(("project:" + self.c["project"], {"role": f"projects/{self.c['project']}/roles/{role}", "member": DEPLOYER})
                        for role in ("lwcExportRoleReadback", "lwcExportProjectPolicyReadback"))
        required.append(("serviceAccount:" + self.c["signing_service_account"],
                         {"role": "projects/llm-wiki-cloud/roles/lwcExportSignerPolicyReadback", "member": DEPLOYER}))
        if any((target, json.dumps(binding, sort_keys=True)) not in policy_bindings for target, binding in required):
            raise ProvisionError("original owner evidence does not prove the unchanged DEV IAM contract")

    def owner_prerequisites(self) -> tuple[str, str, str, str, str]:
        c = self.c
        self.verify_service_account(c["runtime_service_account"])
        self.verify_service_account(c["signing_service_account"])
        roles = tuple(self.verify_custom_role(key) for key in
                      ("objectLister", "sourceReader", "archiveWriter", "blobSigner", "readyArchiveReader"))
        verifier_roles = self.verify_verifier_roles()
        self.bucket_ready()
        runtime = "serviceAccount:" + c["runtime_service_account"]
        datastore = {"role": "roles/datastore.user", "member": runtime, "condition": {
            "title": "lwc344-export-worker-dev-firestore", "description": "Limit Export worker to the DEV Firestore database",
            "expression": DB_CONDITION}}
        self.verify_policy("project:" + c["project"],
            ["projects", "get-iam-policy", c["project"], "--format=json", "--quiet"], datastore)
        bindings = [
            {"role": roles[0], "member": runtime},
            {"role": roles[1], "member": runtime, "condition": {
                "title": "lwc344-export-source-objects", "description": "Read Export source objects only", "expression": STORAGE_USERS}},
            {"role": roles[2], "member": runtime, "condition": {
                "title": "lwc344-export-archive-objects", "description": "Manage Export temporary and ready archives only", "expression": STORAGE_EXPORTS}},
            ready_archive_binding(roles[4], c["signing_service_account"]),
        ]
        for desired in bindings:
            self.verify_policy("bucket:" + c["bucket"],
                ["storage", "buckets", "get-iam-policy", f"gs://{c['bucket']}", "--format=json", "--quiet"], desired)
        signer_binding = {"role": roles[3], "member": "serviceAccount:" + c["bff_service_account"]}
        self.verify_policy("serviceAccount:" + c["signing_service_account"],
            ["iam", "service-accounts", "get-iam-policy", c["signing_service_account"], "--project", c["project"], "--format=json", "--quiet"], signer_binding)
        for key in ("roleReadback", "projectPolicyReadback"):
            self.verify_policy("project:" + c["project"],
                ["projects", "get-iam-policy", c["project"], "--format=json", "--quiet"],
                {"role": verifier_roles[key], "member": DEPLOYER})
        self.verify_policy("serviceAccount:" + c["signing_service_account"],
            ["iam", "service-accounts", "get-iam-policy", c["signing_service_account"], "--project", c["project"], "--format=json", "--quiet"],
            {"role": verifier_roles["signerPolicyReadback"], "member": DEPLOYER})
        return roles

    def run_owner_bootstrap(self) -> None:
        c = self.c
        self.ensure_service_account(c["runtime_service_account"], "lwc-export-worker-dev", "LWC DEV Export Worker")
        self.ensure_service_account(c["signing_service_account"], "lwc-export-signer-dev", "LWC DEV Export Signing")
        list_role = self.ensure_custom_role("objectLister")
        source_role = self.ensure_custom_role("sourceReader")
        archive_role = self.ensure_custom_role("archiveWriter")
        signer_role = self.ensure_custom_role("blobSigner")
        ready_archive_role = self.ensure_custom_role("readyArchiveReader")
        verifier_roles = self.ensure_verifier_roles()
        self.bucket_ready()
        self.apply_iam(list_role, source_role, archive_role, signer_role, ready_archive_role)
        self.apply_verifier_iam(verifier_roles)
        self.evidence["result"] = "owner_bootstrap_applied_and_read_back"
        self.save()

    def run_workflow(self, sha: str) -> None:
        self.evidence["source"]["sha"] = sha
        configured_account = os.getenv("WIF_SERVICE_ACCOUNT", "")
        if configured_account != DEPLOYER.removeprefix("serviceAccount:"):
            raise ProvisionError("workflow identity differs from the reviewed DEV deployer service account")
        self.owner_prerequisites()
        image = self.build_image(sha)
        self.ensure_job(image)
        self.apply_job_iam()
        self.evidence["result"] = "workflow_deployed_and_read_back"
        self.save()


def main() -> int:
    try:
        parser = argparse.ArgumentParser()
        modes = parser.add_mutually_exclusive_group()
        modes.add_argument("--owner-bootstrap", action="store_true")
        modes.add_argument("--cleanup-verifier-grants", action="store_true")
        modes.add_argument("--continue-existing-job-evidence", type=Path)
        parser.add_argument("--workflow-evidence", type=Path)
        parser.add_argument("--owner-source-repair-evidence", type=Path)
        parser.add_argument("--continuation-run-id")
        parser.add_argument("--owner-source-sha")
        args = parser.parse_args()
        if args.workflow_evidence and not args.cleanup_verifier_grants:
            raise ProvisionError("--workflow-evidence is used only with --cleanup-verifier-grants")
        if args.cleanup_verifier_grants and not args.workflow_evidence:
            raise ProvisionError("cleanup requires --workflow-evidence from a successful matching DEV workflow run")
        if args.owner_source_repair_evidence and not args.cleanup_verifier_grants:
            raise ProvisionError("--owner-source-repair-evidence is only used with --cleanup-verifier-grants")
        if (args.continuation_run_id or args.owner_source_sha) and not args.continue_existing_job_evidence:
            raise ProvisionError("continuation run and owner source are required only with --continue-existing-job-evidence")
        if args.continue_existing_job_evidence and (not args.continuation_run_id or not args.owner_source_sha):
            raise ProvisionError("existing Job continuation requires its exact prior run ID and original owner source SHA")
        c = load_contract()
        sha, ref = os.getenv("SOURCE_SHA", ""), os.getenv("SOURCE_REF", "")
        if ref != "refs/heads/develop" or not re.fullmatch(r"[0-9a-f]{40}", sha):
            raise ProvisionError("provisioning is allowed only for an exact develop commit SHA")
        checked_out = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
        if checked_out != sha:
            raise ProvisionError("checked out source does not match the workflow SHA")
        evidence_path = EVIDENCE.with_name("exportjob-dev-source-repair-evidence.json") if args.owner_source_repair_evidence else EVIDENCE
        provisioner = Provisioner(c, evidence_path=evidence_path)
        if args.owner_bootstrap:
            provisioner.run_owner_bootstrap()
        elif args.cleanup_verifier_grants:
            provisioner.cleanup_verifier_grants(args.workflow_evidence, args.owner_source_repair_evidence)
        elif args.continue_existing_job_evidence:
            try:
                prior = json.loads(args.continue_existing_job_evidence.read_text())
            except (OSError, json.JSONDecodeError) as error:
                raise ProvisionError("prior workflow evidence is unreadable; continuation stopped") from error
            provisioner.continue_existing_job(prior, run_id=args.continuation_run_id,
                                              owner_source_sha=args.owner_source_sha, sha=sha)
        else:
            provisioner.run_workflow(sha)
        return 0
    except (ProvisionError, OSError, ValueError, KeyError, json.JSONDecodeError) as error:
        print(f"DEV Export provisioning stopped: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
