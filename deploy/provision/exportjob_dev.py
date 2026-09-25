#!/usr/bin/env python3
"""First-time DEV Export Job provisioning; run only from the reviewed develop workflow."""
from __future__ import annotations

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
    return " ".join(value.split())[:500]


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
            if allow_missing and ("NOT_FOUND" in text or "not found" in text.lower()):
                return None
            raise ProvisionError(f"gcloud {args[0]} failed ({result.returncode}); inspect the workflow log")
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
        timeout_matches = timeout in (expected["timeout"], "82800s", 82800)
        return (len(containers) == 1 and containers[0].get("image") == expected["image"] and
                task.get(account_key) == expected["service_account"] and env == expected["env"] and
                timeout_matches and task.get("maxRetries") == expected["max_retries"] and
                execution.get("parallelism") == expected["parallelism"] and execution.get("taskCount") == expected["tasks"])

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

    def ensure_custom_role(self, role_key: str) -> str:
        c = self.c
        role_id = {"objectLister": "lwcExportObjectLister", "sourceReader": "lwcExportSourceReader",
                   "archiveWriter": "lwcExportArchiveWriter", "blobSigner": "lwcExportBlobSigner",
                   "readyArchiveReader": "lwcExportReadyArchiveReader"}[role_key]
        title = {"objectLister": "LWC DEV Export Object List", "sourceReader": "LWC DEV Export Source Reader",
                 "archiveWriter": "LWC DEV Export Archive Writer", "blobSigner": "LWC DEV Export Blob Signer",
                 "readyArchiveReader": "LWC DEV Export Ready Archive Reader"}[role_key]
        permissions = c["storage_roles"][role_key]
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
                               "--title", title, "--description", f"LWC-344 DEV Export {role_key}",
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
        job_binding = {"role": "roles/run.jobsExecutorWithOverrides", "member": bff}
        self.policy("job:" + c["job"], ["run", "jobs", "get-iam-policy", c["job"], "--project", project, "--region", c["region"], "--format=json", "--quiet"],
                    ["run", "jobs", "set-iam-policy", c["job"], "--project", project, "--region", c["region"]], job_binding)
        signer_binding = {"role": signer_role, "member": bff}
        signer = c["signing_service_account"]
        self.policy("serviceAccount:" + signer,
                    ["iam", "service-accounts", "get-iam-policy", signer, "--project", project, "--format=json", "--quiet"],
                    ["iam", "service-accounts", "set-iam-policy", signer, "--project", project], signer_binding)

    def run_all(self, sha: str) -> None:
        self.evidence["source"]["sha"] = sha
        image = self.build_image(sha)
        c = self.c
        self.ensure_service_account(c["runtime_service_account"], "lwc-export-worker-dev", "LWC DEV Export Worker")
        self.ensure_service_account(c["signing_service_account"], "lwc-export-signer-dev", "LWC DEV Export Signing")
        self.ensure_job(image)
        list_role = self.ensure_custom_role("objectLister")
        source_role = self.ensure_custom_role("sourceReader")
        archive_role = self.ensure_custom_role("archiveWriter")
        signer_role = self.ensure_custom_role("blobSigner")
        ready_archive_role = self.ensure_custom_role("readyArchiveReader")
        self.bucket_ready()
        self.apply_iam(list_role, source_role, archive_role, signer_role, ready_archive_role)
        self.evidence["result"] = "provisioned_and_read_back"
        self.save()


def main() -> int:
    try:
        c = load_contract()
        sha, ref = os.getenv("SOURCE_SHA", ""), os.getenv("SOURCE_REF", "")
        if ref != "refs/heads/develop" or not re.fullmatch(r"[0-9a-f]{40}", sha):
            raise ProvisionError("provisioning is allowed only for an exact develop commit SHA")
        checked_out = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
        if checked_out != sha:
            raise ProvisionError("checked out source does not match the workflow SHA")
        Provisioner(c).run_all(sha)
        return 0
    except (ProvisionError, OSError, ValueError, KeyError, json.JSONDecodeError) as error:
        print(f"DEV Export provisioning stopped: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
