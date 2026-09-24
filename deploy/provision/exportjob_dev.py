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

ROOT = Path(__file__).resolve().parents[2]
CONTRACT = ROOT / "deploy/provision/exportjob-dev.json"
EVIDENCE = ROOT / ".provision/exportjob-dev-evidence.json"
MAX_ETAG_ATTEMPTS = 5
IMAGE_REPO = "llm-wiki-bff-export-job"
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
            "blobSigner": ["iam.serviceAccounts.signBlob"]}:
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


class Provisioner:
    def __init__(self, config: dict, run=_run, evidence_path: Path = EVIDENCE):
        self.c = config
        self.run = run
        self.evidence_path = evidence_path
        self.evidence = {
            "schema": "lwc-344-exportjob-dev-provision-v1",
            "source": {"ref": os.getenv("SOURCE_REF", ""), "sha": os.getenv("SOURCE_SHA", "")},
            "target": {"environment": config["environment"], "project": config["project"], "region": config["region"]},
            "image": None, "resources": {}, "policies": [],
            "inverse": "Remove only bindings and resources marked created_by_this_run; inspect consumers before deleting new Job/service accounts/custom role.",
        }

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
        self.gcloud("builds", "submit", "apps/bff", "--project", self.c["project"],
                    "--config", "apps/bff/cloudbuild-exportjob.yaml", "--substitutions", f"_IMAGE={tag}", "--quiet")
        digest = self.gcloud("artifacts", "docker", "images", "describe", tag, "--project", self.c["project"],
                             "--format=value(image_summary.digest)", "--quiet")
        digest = (digest or "").strip()
        if not re.fullmatch(r"sha256:[0-9a-f]{64}", digest):
            raise ProvisionError("Cloud Build image digest read-back was invalid")
        image = f"{self.c['artifact_registry']}/{IMAGE_REPO}@{digest}"
        self.evidence["image"] = image
        self.save()
        return image

    def ensure_service_account(self, email: str, name: str, display_name: str) -> None:
        project = self.c["project"]
        description = self.gcloud("iam", "service-accounts", "describe", email, "--project", project, "--format=json", "--quiet", allow_missing=True)
        created = False
        if description is None:
            result = self.run(["gcloud", "iam", "service-accounts", "create", name, "--project", project,
                               "--display-name", display_name, "--description", "LWC-344 DEV Export", "--quiet"])
            if result.returncode:
                # A timed-out create may have been accepted; exact read-back is the recovery check.
                description = self.gcloud("iam", "service-accounts", "describe", email, "--project", project, "--format=json", "--quiet", allow_missing=True)
                if description is None:
                    raise ProvisionError("service account create did not converge; rerun after reviewing the workflow log")
            else:
                created = True
                description = self.gcloud("iam", "service-accounts", "describe", email, "--project", project, "--format=json", "--quiet")
        actual = json.loads(description)
        if actual.get("email") != email or actual.get("name", "").split("/")[-1] != email:
            raise ProvisionError(f"existing service account {email} is partial or mismatched")
        self.evidence["resources"][email] = {"kind": "serviceAccount", "created_by_this_run": created,
                                                "email": email, "inverse": "delete only after confirming no consumers"}
        self.save()

    def image_and_job_config(self, image: str) -> dict:
        c = self.c
        return {"image": image, "service_account": c["runtime_service_account"], "env": {
            "GCP_PROJECT": c["project"], "BUCKET": c["bucket"], "FIRESTORE_DATABASE_ID": c["database"],
            "EXPORT_SIGNING_SERVICE_ACCOUNT": c["signing_service_account"]},
            "timeout": c["job_timeout"], "max_retries": c["max_retries"],
            "parallelism": c["parallelism"], "tasks": c["tasks"]}

    def job_matches(self, raw: str, expected: dict) -> bool:
        job = json.loads(raw)
        template = job.get("template", {}).get("template", {})
        containers = template.get("containers", [])
        env = {entry.get("name"): entry.get("value") for entry in (containers[0].get("env", []) if len(containers) == 1 else [])}
        timeout = template.get("timeout")
        timeout_matches = timeout in (expected["timeout"], "82800s")
        return (len(containers) == 1 and containers[0].get("image") == expected["image"] and
                template.get("serviceAccount") == expected["service_account"] and env == expected["env"] and
                timeout_matches and template.get("maxRetries") == expected["max_retries"] and
                template.get("parallelism") == expected["parallelism"] and template.get("taskCount") == expected["tasks"])

    def ensure_job(self, image: str) -> None:
        c = self.c
        args = ("run", "jobs", "describe", c["job"], "--project", c["project"], "--region", c["region"], "--format=json", "--quiet")
        raw = self.gcloud(*args, allow_missing=True)
        created = False
        desired = self.image_and_job_config(image)
        if raw is None:
            command = ["gcloud", "run", "jobs", "create", c["job"], "--project", c["project"], "--region", c["region"],
                       "--image", image, "--service-account", c["runtime_service_account"], "--task-timeout", c["job_timeout"],
                       "--max-retries", str(c["max_retries"]), "--parallelism", str(c["parallelism"]), "--tasks", str(c["tasks"]),
                       "--set-env-vars", "^|^" + "|".join(f"{key}={value}" for key, value in desired["env"].items()), "--quiet"]
            result = self.run(command)
            if result.returncode:
                raw = self.gcloud(*args, allow_missing=True)
                if raw is None:
                    raise ProvisionError("Job create did not converge; rerun after reviewing the workflow log")
            else:
                raw, created = self.gcloud(*args), True
        if not self.job_matches(raw or "{}", desired):
            raise ProvisionError("existing Export Job is partial or differs from the reviewed DEV runtime contract")
        self.evidence["resources"][c["job"]] = {"kind": "cloudRunJob", "created_by_this_run": created,
                                                 "config": desired, "inverse": "delete only after confirming no consumers"}
        self.save()

    def ensure_custom_role(self, role_key: str) -> str:
        c = self.c
        role_id = {"objectLister": "lwcExportObjectLister", "sourceReader": "lwcExportSourceReader",
                   "archiveWriter": "lwcExportArchiveWriter", "blobSigner": "lwcExportBlobSigner"}[role_key]
        title = {"objectLister": "LWC DEV Export Object List", "sourceReader": "LWC DEV Export Source Reader",
                 "archiveWriter": "LWC DEV Export Archive Writer", "blobSigner": "LWC DEV Export Blob Signer"}[role_key]
        permissions = c["storage_roles"][role_key]
        existing = self.gcloud("iam", "roles", "describe", role_id, "--project", c["project"], "--format=json", "--quiet", allow_missing=True)
        created = False
        if existing is None:
            result = self.run(["gcloud", "iam", "roles", "create", role_id, "--project", c["project"],
                               "--title", title, "--description", f"LWC-344 DEV Export {role_key}",
                               "--permissions", ",".join(permissions), "--stage", "GA", "--quiet"])
            if result.returncode:
                existing = self.gcloud("iam", "roles", "describe", role_id, "--project", c["project"], "--format=json", "--quiet", allow_missing=True)
                if existing is None:
                    raise ProvisionError("list-only custom role create did not converge")
            else:
                existing, created = self.gcloud("iam", "roles", "describe", role_id, "--project", c["project"], "--format=json", "--quiet"), True
        role = json.loads(existing or "{}")
        if sorted(role.get("includedPermissions", [])) != sorted(permissions) or role.get("stage") != "GA":
            raise ProvisionError(f"existing custom role {role_id} is partial or has extra permissions")
        name = f"projects/{c['project']}/roles/{role_id}"
        self.evidence["resources"][name] = {"kind": "customRole", "created_by_this_run": created,
                                            "included_permissions": permissions,
                                            "inverse": "delete only after confirming no consumers"}
        self.save()
        return name

    def bucket_ready(self) -> None:
        raw = self.gcloud("storage", "buckets", "describe", f"gs://{self.c['bucket']}", "--format=json", "--quiet")
        bucket = json.loads(raw or "{}")
        if not bucket.get("iamConfiguration", {}).get("uniformBucketLevelAccess", {}).get("enabled"):
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
            after = json.loads(self.gcloud(*get_args) or "{}")
            if not self.policy_has(after, desired):
                entry.update(status="readback_missing_binding", after_etag=after.get("etag"), after_policy=after)
                self.save()
                raise ProvisionError(f"IAM read-back for {target} is missing the exact requested binding")
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

    def apply_iam(self, list_role: str, source_role: str, archive_role: str, signer_role: str) -> None:
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
        self.bucket_ready()
        self.apply_iam(list_role, source_role, archive_role, signer_role)
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
