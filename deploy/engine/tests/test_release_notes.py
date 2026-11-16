import copy
import hashlib
import json
import os
import subprocess
import sys
import unittest
from pathlib import Path
from tempfile import TemporaryDirectory
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import release_notes
import release_notes_capture
import release_notes_publish


W_PLAN_SOURCE = "f" * 40
V_COMMIT = "e" * 40
V_IMAGE = "asia-east1-docker.pkg.dev/project/repo/bff@sha256:" + "a" * 64
W_IMAGE = "asia-east1-docker.pkg.dev/project/repo/bff@sha256:" + "b" * 64


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":")).encode()


def run_capture_cli_with_service_url_failure(directory, *, operation, action, run_id="701", artifact_id="",
                                            artifact_digest=""):
    fake_bin = directory / "fake-bin"
    fake_bin.mkdir(exist_ok=True)
    gcloud = fake_bin / "gcloud"
    plan = json.loads((directory / "plan.json").read_bytes())
    state = json.loads((directory / "state.json").read_bytes())
    receipt = json.loads((directory / "receipts" / "bff.json").read_bytes())
    service = plan["normalized"]["bff"]["service_name"]
    entry = state["components"]["bff"]
    target = entry["prior"] if operation == "rollback" else entry["candidate"]
    revision = target["revision"]
    image = receipt["artifact"]["image"]
    service_object = {
        "metadata": {"name": service, "uid": "uid-fixture", "generation": max(state.get("sequence", 1), 1)},
        "status": {"traffic": [{"revisionName": revision, "percent": 100}]},
    }
    revision_object = {
        "metadata": {"name": revision},
        "status": {"conditions": [{"type": "Ready", "status": "True"}], "imageDigest": image},
        "spec": {"containers": [{"image": image}]},
    }
    gcloud.write_text(
        "#!/usr/bin/env python3\n"
        "import json, sys\n"
        "if len(sys.argv) > 2 and sys.argv[2] == 'services': print(" + repr(json.dumps(service_object)) + ")\n"
        "elif len(sys.argv) > 2 and sys.argv[2] == 'revisions': print(" + repr(json.dumps(revision_object)) + ")\n"
        "else: sys.exit(1)\n"
    )
    gcloud.chmod(0o755)
    environment = os.environ.copy()
    environment.update({
        "PATH": str(fake_bin) + os.pathsep + environment.get("PATH", ""),
        "GOOGLE_APPLICATION_CREDENTIALS": "/dev/null",
        "GCE_METADATA_HOST": "127.0.0.1:9",
        "CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE": "/dev/null",
        "HTTP_PROXY": "http://127.0.0.1:9",
        "HTTPS_PROXY": "http://127.0.0.1:9",
        "NO_PROXY": "127.0.0.1,localhost",
        "http_proxy": "http://127.0.0.1:9",
        "https_proxy": "http://127.0.0.1:9",
        "no_proxy": "127.0.0.1,localhost",
    })
    script = Path(__file__).resolve().parents[1] / "release_notes_capture.py"
    args = [sys.executable, str(script), "--directory", str(directory), "--operation", operation,
            "--run-id", run_id, "--run-attempt", "1", "--environment", "dev",
            "--release-notes-action", action, "--product-status", "released"]
    if artifact_id:
        args.extend(("--artifact-id", artifact_id,
                     "--artifact-digest", artifact_digest or "sha256:" + "c" * 64))
    return subprocess.run(args, cwd=Path(__file__).resolve().parents[3], env=environment,
                          capture_output=True, text=True, check=False)


def plan_fixture():
    return {
        "id": "release-plan-w",
        "tag": "v2026.11.5",
        "source": W_PLAN_SOURCE,
        "identities": {"bff": {"profile": "bff", "digest": "w-inputs"}},
        "selected": ["bff"],
        "normalized": {"environment": "development", "gcp": {"project_id": "project", "region": "asia-east1"},
                       "bff": {"service_name": "svc"}},
    }


def state_fixture(plan, *, status="rolled_back", prior_image=V_IMAGE, revision="rev-v", generation=43):
    prior = {"revision": "rev-v", "image": prior_image,
             "traffic": [{"revisionName": "rev-v", "percent": 100}]}
    candidate = {"bff_config": {"status": "published",
                                 "version_resource": "projects/project/secrets/bff-config-dev/versions/42"},
                 "revision": "rev-w"}
    component_status = "rolled_back" if status == "rolled_back" else "verified"
    return {"plan": plan["id"], "status": status, "sequence": generation,
            "components": {"bff": {"status": component_status, "prior": prior, "candidate": candidate}}}


def operation_fixture(operation="rollback", *, service_generation=43, prior_image=V_IMAGE,
                      state_status="rolled_back", result_status=None, revision="rev-v", product_version="1.2.3",
                      sidecar_image=None):
    if state_status == "rolled_back" and operation != "rollback":
        state_status = "success"
    plan = plan_fixture()
    state = state_fixture(plan, status=state_status, prior_image=prior_image, revision=revision,
                          generation=service_generation)
    state_bytes = canonical(state)
    plan_bytes = canonical(plan)
    state_component = state["components"]["bff"]
    status_value = result_status or ("rolled_back" if operation == "rollback" else "success")
    result = {"release": plan["tag"], "attempt": plan["id"], "stage": status_value, "status": status_value,
              "component": "bff", "prior": state_component["prior"], "candidate": state_component["candidate"],
              "expected": {"source": plan["source"], "identity": hashlib.sha256(canonical(plan["identities"]["bff"])).hexdigest()},
              "observed": {"component_status": state_component["status"]},
              "last_verified_checkpoint": state["sequence"]}
    result_bytes = canonical(result)
    active_revision = state_component["prior"]["revision"] if operation == "rollback" else state_component["candidate"]["revision"]
    active_image = sidecar_image or (state_component["prior"]["image"] if operation == "rollback" else W_IMAGE)
    build = {"environment": "dev", "product_version": product_version, "commit": V_COMMIT if operation == "rollback" else W_PLAN_SOURCE,
             "branch": "develop", "tag": "v2026.11.4" if operation == "rollback" else "v2026.11.5",
             "image_tag": "bff-v" if operation == "rollback" else "bff-w", "service": "svc", "revision": active_revision}
    sidecar = {"schema_version": 1, "environment": "dev", "operation": operation, "run_id": "701", "run_attempt": 1,
               "plan_sha256": hashlib.sha256(plan_bytes).hexdigest(), "state_sha256": hashlib.sha256(state_bytes).hexdigest(),
               "result_sha256": hashlib.sha256(result_bytes).hexdigest(), "service": "svc", "service_uid": "uid-1",
               "service_generation": service_generation, "revision": active_revision, "image_digest": active_image.rsplit("@", 1)[-1],
               "traffic": [{"revisionName": active_revision, "percent": 100}], "build_info": build}
    receipt = {"schema": 2, "component": "bff", "identity": plan["identities"]["bff"],
               "build_sha": plan["source"], "artifact": {"image": W_IMAGE}}
    return plan_bytes, state_bytes, result_bytes, canonical(sidecar), canonical(receipt), sidecar


def schema4_fixture(fixture, release_version, *, plan_id=None, run_id="701"):
    plan, state, result, sidecar = (json.loads(fixture[index]) for index in range(4))
    plan.update(schema=4, release_version=release_version)
    if plan_id:
        plan["id"] = plan_id
    state["plan"] = plan["id"]
    result["attempt"] = plan["id"]
    plan_bytes, state_bytes, result_bytes = canonical(plan), canonical(state), canonical(result)
    sidecar.update(run_id=run_id,
                   plan_sha256=release_notes.digest_bytes(plan_bytes),
                   state_sha256=release_notes.digest_bytes(state_bytes),
                   result_sha256=release_notes.digest_bytes(result_bytes))
    return plan_bytes, state_bytes, result_bytes, canonical(sidecar), fixture[4]


def frontend_reuse_fixture(*, run_id="702", release_version="2026.11.5", deployment_id="frontend-deploy-702"):
    base = operation_fixture(operation="release", service_generation=44, revision="rev-w", product_version="1.0.0")
    plan, state, result, sidecar = (json.loads(base[index]) for index in range(4))
    identity = {"profile": "frontend", "digest": "frontend-inputs-" + run_id}
    plan.update(schema=4, id="frontend-only-plan-" + run_id, release_version=release_version,
                selected=["frontend"], identities={"frontend": identity})
    state.update(plan=plan["id"], status="success", sequence=44,
                 components={"frontend": {"status": "verified", "candidate": {"deployment_id": deployment_id}}})
    result.update(release=plan["tag"], attempt=plan["id"], stage="success", status="success",
                  component="frontend", candidate=state["components"]["frontend"]["candidate"], prior=None,
                  expected={"source": plan["source"], "identity": hashlib.sha256(canonical(identity)).hexdigest()},
                  observed={"component_status": "verified"}, last_verified_checkpoint=state["sequence"])
    plan_bytes, state_bytes, result_bytes = canonical(plan), canonical(state), canonical(result)
    sidecar.update(operation="release", run_id=run_id,
                   plan_sha256=release_notes.digest_bytes(plan_bytes),
                   state_sha256=release_notes.digest_bytes(state_bytes),
                   result_sha256=release_notes.digest_bytes(result_bytes))
    receipt = {"schema": 2, "component": "frontend", "identity": identity, "build_sha": plan["source"],
               "artifact": {"deployment_id": deployment_id}}
    return plan_bytes, state_bytes, result_bytes, canonical(sidecar), None, {"frontend": canonical(receipt)}


def normalized_marker(**kwargs):
    fixture = operation_fixture(**kwargs)
    return release_notes.normalize_operation(*fixture[:4], environment="dev",
        workflow_operation=kwargs.get("operation", "rollback"), run_id="701", run_attempt=1,
        service="svc", receipt_bytes=fixture[4])[0]


def initial_schema2_migration_fixture():
    old_marker = normalized_marker(operation="deploy", service_generation=43,
                                   revision="rev-v", product_version="1.0.0")
    old_marker_token = json.dumps(old_marker, ensure_ascii=False, indent=2).encode("utf-8")
    index_bytes = b'{ "schema_version": 1, "versions": [], "current_deployment": ' + old_marker_token + b' }'
    fixture = list(operation_fixture(operation="release", service_generation=44,
                                     revision="rev-w", product_version="1.0.0"))
    plan = json.loads(fixture[0])
    plan["schema"] = 4
    plan["release_version"] = release_notes.PUBLIC_RELEASE_VERSION
    plan_bytes = canonical(plan)
    sidecar = json.loads(fixture[3])
    sidecar["plan_sha256"] = release_notes.digest_bytes(plan_bytes)
    fixture[0] = plan_bytes
    fixture[3] = canonical(sidecar)
    marker, _ = release_notes.normalize_operation(
        *fixture[:4], environment="dev", workflow_operation="release", run_id="701",
        run_attempt=1, service="svc", receipt_bytes=fixture[4])
    release_notes.bind_previous_release(marker, old_marker, old_marker_token)
    store = MemoryNotesStore()
    store.index = index_bytes
    store.generation = 5
    store.active_marker = marker
    return store, marker, release_notes.digest_bytes(plan_bytes), old_marker_token


def multi_component_fixture(operation):
    fixture = list(operation_fixture(operation=operation, service_generation=44 if operation != "rollback" else 43,
                                     revision="rev-w" if operation != "rollback" else "rev-v",
                                     product_version="9.0.0" if operation != "rollback" else "1.2.3"))
    plan, state, result, sidecar = (json.loads(fixture[i]) for i in range(4))
    frontend_identity = {"profile": "frontend", "digest": "frontend-inputs"}
    plan["selected"] = ["bff", "frontend"]
    plan["identities"]["frontend"] = frontend_identity
    state["components"]["frontend"] = {
        "status": "verified" if operation != "rollback" else "rolled_back",
        "prior": {"revision": "frontend-prior"},
        "candidate": {"revision": "frontend-candidate", "image": "frontend-image"},
    }
    result["component"] = "frontend"
    result["expected"]["identity"] = hashlib.sha256(canonical(frontend_identity)).hexdigest()
    result["observed"]["component_status"] = state["components"]["frontend"]["status"]
    result["candidate"] = state["components"]["frontend"]["candidate"]
    if operation == "rollback":
        result["prior"] = state["components"]["frontend"]["prior"]
    plan_bytes, state_bytes, result_bytes = canonical(plan), canonical(state), canonical(result)
    sidecar.update({"plan_sha256": hashlib.sha256(plan_bytes).hexdigest(),
                    "state_sha256": hashlib.sha256(state_bytes).hexdigest(),
                    "result_sha256": hashlib.sha256(result_bytes).hexdigest()})
    fixture[0:4] = [plan_bytes, state_bytes, result_bytes, canonical(sidecar)]
    return tuple(fixture)


def old_v_release_fixture():
    fixture = list(operation_fixture(operation="release", service_generation=42, revision="rev-v",
                                     product_version="1.2.3", sidecar_image=V_IMAGE))
    plan, state, result, sidecar, receipt = (json.loads(fixture[i]) for i in range(5))
    candidate = state["components"]["bff"]["candidate"]
    candidate.update(revision="rev-v")
    result["candidate"] = candidate
    receipt["artifact"]["image"] = V_IMAGE
    sidecar.update(revision="rev-v", image_digest=V_IMAGE.rsplit("@", 1)[-1],
                   traffic=[{"revisionName": "rev-v", "percent": 100}],
                   build_info={"environment": "dev", "product_version": "1.2.3", "commit": V_COMMIT,
                               "branch": "develop", "tag": "v2026.11.4", "image_tag": "bff-v",
                               "service": "svc", "revision": "rev-v"})
    state_bytes, result_bytes = canonical(state), canonical(result)
    sidecar.update(state_sha256=hashlib.sha256(state_bytes).hexdigest(),
                   result_sha256=hashlib.sha256(result_bytes).hexdigest())
    fixture[1:5] = [state_bytes, result_bytes, canonical(sidecar), canonical(receipt)]
    return tuple(fixture)


def note(version="1.2.3", locale="en", changed="Changed"):
    return canonical({"schema_version": 1, "product": "llm-wiki-cloud", "product_version": version, "locale": locale,
                      "entries": [{"title": "Title", "what_changed": changed, "user_benefit": "Benefit",
                                   "where_to_use": "Workspace", "limitations": []}]})


class GenerationConflict(Exception):
    safe_class = "generation-conflict"


class MemoryNotesStore:
    def __init__(self):
        self.index = None
        self.generation = 0
        self.objects = {}
        self.next_generation = 1
        self.active_marker = None
        self.writes = 0

    def read_index(self):
        return self.index, self.generation

    def create_content(self, path, body):
        if path in self.objects:
            if self.objects[path][0] != body:
                raise ValueError("immutable conflict")
            return self.objects[path][1]
        generation = self.next_generation
        self.next_generation += 1
        self.objects[path] = (body, generation)
        return generation

    def read_content(self, path):
        return self.objects[path][0]

    def compare_and_swap_index(self, data, generation):
        if generation != self.generation:
            raise GenerationConflict()
        self.index = data
        self.generation += 1
        self.writes += 1
        return self.generation

    def active_marker_matches(self, marker):
        return self.active_marker == marker


class InterleavingNotesStore(MemoryNotesStore):
    """Apply one competing writer immediately before the caller's first CAS."""

    def __init__(self):
        super().__init__()
        self.contender = None

    def active_marker_matches(self, marker, **_kwargs):
        return super().active_marker_matches(marker)

    def compare_and_swap_index(self, data, generation):
        if self.contender is not None:
            contender, self.contender = self.contender, None
            contender(self)
            raise GenerationConflict()
        return super().compare_and_swap_index(data, generation)


class ReleaseNotesContractTests(unittest.TestCase):
    def normalize(self, operation="rollback", **kwargs):
        fixture = operation_fixture(operation=operation, **kwargs)
        marker, _ = release_notes.normalize_operation(*fixture[:4], environment="dev", workflow_operation=operation,
            run_id="701", run_attempt=1, service="svc", receipt_bytes=fixture[4])
        return marker

    def test_native_w_plan_rollback_normalizes_effective_v_without_mutating_provenance(self):
        fixture = operation_fixture()
        plan_before = fixture[0]
        marker, bundle = release_notes.normalize_operation(*fixture[:4], environment="dev", workflow_operation="rollback",
            run_id="701", run_attempt=1, service="svc", receipt_bytes=fixture[4])
        self.assertEqual(bundle["plan"]["source"], W_PLAN_SOURCE)
        self.assertEqual(bundle["result"]["expected"]["source"], W_PLAN_SOURCE)
        self.assertNotIn("image", bundle["state"]["components"]["bff"]["candidate"])
        self.assertEqual(marker["product_version"], "1.2.3")
        self.assertEqual(marker["revision"], "rev-v")
        self.assertEqual(marker["operation"], "rollback")
        self.assertEqual(marker["service_generation"], 43)
        self.assertEqual(fixture[0], plan_before)

    def test_native_operation_rejects_old_receipt_and_invalid_rollback_shapes(self):
        old_fixture = old_v_release_fixture()
        previous_v, _ = release_notes.normalize_operation(
            *old_fixture[:4], environment="dev", workflow_operation="release", run_id="701",
            run_attempt=1, service="svc", receipt_bytes=old_fixture[4])
        current_rollback_v = self.normalize(service_generation=43)
        store = MemoryNotesStore()
        store.active_marker = current_rollback_v
        self.assertFalse(store.active_marker_matches(previous_v))
        with self.assertRaisesRegex(release_notes.ReleaseNotesError, "release-transition-stale"):
            release_notes.publish_marker_only(store, previous_v)
        for kwargs in ({"result_status": "failed_rolled_back"}, {"state_status": "failed_rolled_back"},
                       {"sidecar_image": W_IMAGE}):
            with self.subTest(kwargs=kwargs), self.assertRaises(release_notes.ReleaseNotesError):
                self.normalize(**kwargs)

    def test_explicit_rollback_wins_on_new_generation_without_semver_ordering(self):
        old_w = self.normalize(operation="deploy", service_generation=42, revision="rev-w", product_version="9.0.0")
        new_rollback_v = self.normalize(service_generation=43, product_version="1.2.3")
        self.assertTrue(release_notes.marker_transition_allowed(old_w, new_rollback_v))
        self.assertFalse(release_notes.marker_transition_allowed(old_w, {**new_rollback_v, "service_generation": 42}))
        self.assertFalse(release_notes.marker_transition_allowed(old_w, {**new_rollback_v, "service_generation": 42, "product_version": "1.2.4"}))
        self.assertTrue(release_notes.marker_transition_allowed(old_w, {**new_rollback_v, "service_uid": "uid-2"}))

    def test_normal_operations_bind_receipt_image_to_readback_with_native_candidate_shape(self):
        for operation in ("release", "deploy", "reactivate", "promote"):
            with self.subTest(operation=operation):
                marker = normalized_marker(operation=operation, revision="rev-w", product_version="9.0.0")
                self.assertEqual(marker["operation"], operation)
                self.assertEqual(marker["revision"], "rev-w")
                fixture = list(operation_fixture(operation=operation, revision="rev-w", product_version="9.0.0"))
                self.assertNotIn("image", json.loads(fixture[1])["components"]["bff"]["candidate"])
                receipt = json.loads(fixture[4])
                receipt["artifact"]["image"] = V_IMAGE
                fixture[4] = canonical(receipt)
                with self.assertRaisesRegex(release_notes.ReleaseNotesError, "selected-target-readback-mismatch"):
                    release_notes.normalize_operation(*fixture[:4], environment="dev", workflow_operation=operation,
                        run_id="701", run_attempt=1, service="svc", receipt_bytes=fixture[4])

                fixture = list(operation_fixture(operation=operation, revision="rev-w", product_version="9.0.0"))
                sidecar = json.loads(fixture[3])
                sidecar["revision"] = "rev-stale"
                sidecar["build_info"]["revision"] = "rev-stale"
                sidecar["traffic"][0]["revisionName"] = "rev-stale"
                fixture[3] = canonical(sidecar)
                with self.assertRaisesRegex(release_notes.ReleaseNotesError, "selected-target-readback-mismatch"):
                    release_notes.normalize_operation(*fixture[:4], environment="dev", workflow_operation=operation,
                        run_id="701", run_attempt=1, service="svc", receipt_bytes=fixture[4])

    def test_native_multi_component_result_can_name_last_engine_component(self):
        for operation in ("release", "rollback"):
            with self.subTest(operation=operation):
                fixture = multi_component_fixture(operation)
                marker, bundle = release_notes.normalize_operation(
                    *fixture[:4], environment="dev", workflow_operation=operation,
                    run_id="701", run_attempt=1, service="svc", receipt_bytes=fixture[4])
                native = release_notes.validate_native_operation(
                    fixture[0], fixture[1], fixture[2], fixture[4], environment="dev",
                    workflow_operation=operation, run_id="701", run_attempt=1)
                self.assertEqual(bundle["result"]["component"], "frontend")
                self.assertEqual(native["result"]["component"], "frontend")
                self.assertEqual(marker["revision"], "rev-v" if operation == "rollback" else "rev-w")

    def test_schema4_frontend_only_release_reuses_fresh_bff_readback_and_selected_receipt(self):
        fixture = frontend_reuse_fixture()
        marker, bundle = release_notes.normalize_operation(
            *fixture[:4], environment="dev", workflow_operation="release", run_id="702", run_attempt=1,
            service="svc", receipt_bytes=fixture[4], component_receipts=fixture[5])
        native = release_notes.validate_native_operation(
            fixture[0], fixture[1], fixture[2], fixture[4], environment="dev",
            workflow_operation="release", run_id="702", run_attempt=1, component_receipts=fixture[5])
        self.assertEqual(bundle["plan"]["selected"], ["frontend"])
        self.assertTrue(native["bff_reused"])
        self.assertEqual(marker["service_generation"], 44)
        self.assertEqual(marker["component_builds"][0]["deployment_id"], "frontend-deploy-702")

        with self.assertRaisesRegex(release_notes.ReleaseNotesError, "selected-component-receipt-unavailable"):
            release_notes.normalize_operation(
                *fixture[:4], environment="dev", workflow_operation="release", run_id="702", run_attempt=1,
                service="svc", receipt_bytes=None)
        wrong_receipts = {"frontend": canonical({"component": "frontend", "identity": {"digest": "wrong"},
                                                  "artifact": {"deployment_id": "frontend-deploy-702"}})}
        with self.assertRaisesRegex(release_notes.ReleaseNotesError, "selected-component-receipt-invalid"):
            release_notes.normalize_operation(
                *fixture[:4], environment="dev", workflow_operation="release", run_id="702", run_attempt=1,
                service="svc", receipt_bytes=None, component_receipts=wrong_receipts)

    def test_bff_reuse_allows_only_same_full_identity_generation_with_newer_selected_run(self):
        store, current, plan_sha, _ = initial_schema2_migration_fixture()
        release_notes.publish_package(
            store, {"en": note(version=release_notes.PUBLIC_RELEASE_VERSION)},
            product_version=release_notes.PUBLIC_RELEASE_VERSION,
            published_at="2026-11-15T01:14:01Z", marker=current, transition_plan_sha256=plan_sha)
        fixture = frontend_reuse_fixture(run_id="702")
        marker, _ = release_notes.normalize_operation(
            *fixture[:4], environment="dev", workflow_operation="release", run_id="702", run_attempt=1,
            service="svc", receipt_bytes=fixture[4], component_receipts=fixture[5])
        denied_store = copy.deepcopy(store)
        denied_store.active_marker = marker
        with self.assertRaisesRegex(release_notes.ReleaseNotesError, "release-transition-stale"):
            release_notes.publish_package(
                denied_store, {"en": note(version="2026.11.5")}, product_version="2026.11.5",
                published_at="2026-11-16T01:00:00Z", marker=copy.deepcopy(marker), bff_reused=False)
        store.active_marker = marker
        published = release_notes.publish_package(
            store, {"en": note(version="2026.11.5")}, product_version="2026.11.5",
            published_at="2026-11-16T01:00:00Z", marker=marker, bff_reused=True)
        self.assertEqual(published.index_generation, store.generation)
        self.assertEqual(release_notes.validate_index(store.index)["current_deployment"]["release_version"],
                         "2026.11.5")

        stale = copy.deepcopy(marker)
        stale["operation_run_id"] = current["operation_run_id"]
        stale["previous_release"] = marker["previous_release"]
        store.active_marker = stale
        with self.assertRaisesRegex(release_notes.ReleaseNotesError, "release-transition-stale"):
            release_notes.publish_marker_only(store, stale)

        changed_bff = copy.deepcopy(marker)
        changed_bff["service_generation"] += 1
        store.active_marker = changed_bff
        with self.assertRaisesRegex(release_notes.ReleaseNotesError, "release-bff-reuse-mismatch"):
            release_notes.publish_marker_only(store, changed_bff, bff_reused=True)

    def test_schema4_rollback_restores_v_identity_with_fresh_operation_generation(self):
        v_fixture = schema4_fixture(operation_fixture(
            operation="release", service_generation=42, revision="rev-v", product_version="1.2.3"),
            "2026.11.3", plan_id="plan-v")
        v_plan, v_state, v_result, v_sidecar = (json.loads(v_fixture[index]) for index in range(4))
        v_state["components"]["bff"]["candidate"]["revision"] = "rev-v"
        v_result["candidate"] = copy.deepcopy(v_state["components"]["bff"]["candidate"])
        v_state_bytes = canonical(v_state)
        v_result_bytes = canonical(v_result)
        v_sidecar.update(revision="rev-v", traffic=[{"revisionName": "rev-v", "percent": 100}],
                         state_sha256=release_notes.digest_bytes(v_state_bytes),
                         result_sha256=release_notes.digest_bytes(v_result_bytes))
        v_sidecar["build_info"]["revision"] = "rev-v"
        v_sidecar["build_info"].update(commit=V_COMMIT, tag="v2026.11.4", image_tag="bff-v")
        v_sidecar["image_digest"] = V_IMAGE.rsplit("@", 1)[-1]
        v_receipt = json.loads(v_fixture[4])
        v_receipt["artifact"]["image"] = V_IMAGE
        v_fixture = (canonical(v_plan), v_state_bytes, v_result_bytes, canonical(v_sidecar), canonical(v_receipt))
        v, _ = release_notes.normalize_operation(
            *v_fixture[:4], environment="dev", workflow_operation="release", run_id="701", run_attempt=1,
            service="svc", receipt_bytes=v_fixture[4])
        w_fixture = schema4_fixture(operation_fixture(
            operation="release", service_generation=43, revision="rev-w", product_version="9.0.0"),
            "2026.11.4", plan_id="plan-w", run_id="702")
        w, _ = release_notes.normalize_operation(
            *w_fixture[:4], environment="dev", workflow_operation="release", run_id="702", run_attempt=1,
            service="svc", receipt_bytes=w_fixture[4])
        release_notes._prepare_marker_transition(w, v)
        rollback_fixture = schema4_fixture(operation_fixture(
            operation="rollback", service_generation=44, revision="rev-v", product_version="1.2.3"),
            "2026.11.4", plan_id="plan-rollback", run_id="703")
        rollback, _ = release_notes.normalize_operation(
            *rollback_fixture[:4], environment="dev", workflow_operation="rollback", run_id="703",
            run_attempt=1, service="svc", receipt_bytes=rollback_fixture[4])
        release_notes._prepare_marker_transition(rollback, w)
        self.assertEqual(rollback["release_version"], "2026.11.3")
        self.assertEqual(rollback["service_generation"], 44)
        self.assertEqual(w["service_generation"], 43)
        self.assertTrue(release_notes.marker_transition_allowed(w, rollback))

        store = MemoryNotesStore()
        store.index = canonical({"schema_version": release_notes.INDEX_SCHEMA_VERSION, "versions": [],
                                 "current_deployment": w, "legacy_aliases": []})
        store.generation = 8
        store.active_marker = rollback
        result = release_notes.publish_marker_only(store, rollback)
        self.assertEqual(result.index_generation, 9)
        self.assertEqual(release_notes.validate_index(store.index)["current_deployment"], rollback)

        old_generation = copy.deepcopy(rollback)
        old_generation["service_generation"] = w["service_generation"]
        with self.assertRaisesRegex(release_notes.ReleaseNotesError, "rollback-target-mismatch"):
            release_notes._prepare_marker_transition(old_generation, w)
        wrong_revision = copy.deepcopy(rollback)
        wrong_revision["revision"] = "rev-other"
        with self.assertRaisesRegex(release_notes.ReleaseNotesError, "rollback-target-mismatch"):
            release_notes._prepare_marker_transition(wrong_revision, w)
        wrong_receipt = json.loads(rollback_fixture[4])
        wrong_receipt["identity"] = {"profile": "bff", "digest": "old-v"}
        with self.assertRaisesRegex(release_notes.ReleaseNotesError, "selected-target-evidence-invalid"):
            release_notes.normalize_operation(
                *rollback_fixture[:4], environment="dev", workflow_operation="rollback", run_id="703",
                run_attempt=1, service="svc", receipt_bytes=canonical(wrong_receipt))

    def test_publish_is_idempotent_and_same_version_correction_preserves_original_time_and_marker(self):
        marker = self.normalize()
        store = MemoryNotesStore()
        store.active_marker = marker
        first = release_notes.publish_package(store, {"en": note()}, product_version="1.2.3",
            published_at="2026-11-14T10:00:00Z", marker=marker)
        self.assertEqual(first.index_generation, 1)
        original_index = json.loads(store.index)
        replay = release_notes.publish_package(store, {"en": note()}, product_version="1.2.3",
            published_at="2026-11-14T12:00:00Z", marker=marker)
        self.assertEqual(replay.index_generation, first.index_generation)
        self.assertEqual(store.writes, 1)
        correction = release_notes.publish_package(store, {"en": note(changed="Corrected")}, product_version="1.2.3",
            published_at="2026-11-14T12:00:00Z", marker=marker)
        corrected = json.loads(store.index)
        self.assertEqual(len(corrected["versions"]), 1)
        self.assertEqual(corrected["versions"][0]["published_at"], original_index["versions"][0]["published_at"])
        self.assertEqual(corrected["current_deployment"], original_index["current_deployment"])
        self.assertGreater(correction.index_generation, first.index_generation)

    def test_schema4_migration_binds_public_version_alias_and_exact_prior_marker_bytes(self):
        store, marker, plan_sha256, old_marker_token = initial_schema2_migration_fixture()
        marker_before = copy.deepcopy(marker)
        result = release_notes.publish_package(
            store, {"en": note(version=release_notes.PUBLIC_RELEASE_VERSION)},
            product_version=release_notes.PUBLIC_RELEASE_VERSION,
            published_at="2026-11-15T01:14:01Z", marker=marker,
            transition_plan_sha256=plan_sha256)
        self.assertEqual(marker, marker_before)
        index = release_notes.validate_index(store.index)
        created = index["legacy_aliases"][0]
        self.assertEqual(index["schema_version"], release_notes.INDEX_SCHEMA_VERSION)
        self.assertEqual(index["current_deployment"]["product_version"], "1.0.0")
        self.assertEqual(index["current_deployment"]["release_version"], release_notes.PUBLIC_RELEASE_VERSION)
        self.assertEqual(created["source_marker_sha256"], release_notes.digest_bytes(old_marker_token))
        self.assertEqual(created["source_index_generation"], 5)
        self.assertEqual(created["transition_plan_sha256"], plan_sha256)
        self.assertEqual(created["transition_run_id"], "701")
        self.assertEqual(created["creation_marker_sha256"], release_notes.digest_bytes(canonical(marker)))
        self.assertEqual(result.legacy_alias_receipt["action"], "created")
        self.assertEqual(result.legacy_alias_receipt["index_generation_before"], 5)
        self.assertEqual(result.legacy_alias_receipt["index_generation_after"], 6)

        carried_alias = copy.deepcopy(created)
        prior_bytes = release_notes.current_deployment_token_bytes(store.index)
        next_marker = copy.deepcopy(marker)
        next_marker.update(operation="deploy", operation_run_id="702", service_generation=45,
                           revision="rev-next", deployment_evidence_digest="sha256:" + "f" * 64)
        release_notes.bind_previous_release(next_marker, index["current_deployment"], prior_bytes)
        store.active_marker = next_marker
        carry_result = release_notes.publish_marker_only(
            store, next_marker, transition_plan_sha256="a" * 64)
        carried_index = release_notes.validate_index(store.index)
        self.assertEqual(carried_index["legacy_aliases"], [carried_alias])
        self.assertEqual(carry_result.legacy_alias_receipt["action"], "carried")
        self.assertEqual(carry_result.legacy_alias_receipt["alias_record_sha256"], carried_alias["alias_record_sha256"])

    def test_initial_alias_creation_requires_publish_or_explicit_skip_and_resend_cannot_create_it(self):
        store, marker, plan_sha256, _ = initial_schema2_migration_fixture()
        with self.assertRaisesRegex(release_notes.ReleaseNotesError, "legacy-alias-creation-not-authorized"):
            release_notes.publish_marker_only(store, marker, transition_plan_sha256=plan_sha256)
        self.assertEqual((store.generation, store.writes), (5, 0))
        denied = copy.deepcopy(store)
        with self.assertRaisesRegex(release_notes.ReleaseNotesError, "legacy-alias-creation-not-authorized"):
            release_notes.publish_package(
                denied, {"en": note(version=release_notes.PUBLIC_RELEASE_VERSION)},
                product_version=release_notes.PUBLIC_RELEASE_VERSION,
                published_at="2026-11-15T01:14:01Z", marker=marker,
                transition_plan_sha256=plan_sha256, allow_alias_creation=False)
        self.assertEqual((denied.generation, denied.writes), (5, 0))

        skipped_store, skipped_marker, skipped_plan_sha256, _ = initial_schema2_migration_fixture()
        skip_result = release_notes.publish_marker_only(
            skipped_store, skipped_marker, transition_plan_sha256=skipped_plan_sha256,
            allow_alias_creation=True)
        skipped_index = release_notes.validate_index(skipped_store.index)
        self.assertEqual(skipped_index["current_deployment"]["release_version"], release_notes.PUBLIC_RELEASE_VERSION)
        self.assertEqual(skipped_index["legacy_aliases"][0]["creation_marker_sha256"],
                         release_notes.digest_bytes(canonical(skipped_index["current_deployment"])))
        self.assertEqual(skip_result.legacy_alias_receipt["action"], "created")

        history_store, _, _, _ = initial_schema2_migration_fixture()
        original_current = json.loads(history_store.index)["current_deployment"]
        history = release_notes.publish_package(
            history_store, {"en": note(version="2026.11.1")}, product_version="2026.11.1",
            published_at="2026-11-01T22:58:30+08:00", marker=None, historical=True,
            allow_alias_creation=False)
        history_index = release_notes.validate_index(history_store.index)
        self.assertEqual(history_index["current_deployment"], original_current)
        self.assertNotIn("legacy_aliases", history_index)
        self.assertIsNone(history.legacy_alias_receipt)

    def test_alias_fixed_legacy_row_rejects_resend_correction_but_keeps_exact_retry_and_canonical_edits(self):
        store, marker, plan_sha, _ = initial_schema2_migration_fixture()
        store.active_marker = marker
        release_notes.publish_package(
            store, {"en": note(version=release_notes.PUBLIC_RELEASE_VERSION)},
            product_version=release_notes.PUBLIC_RELEASE_VERSION,
            published_at="2026-11-15T01:14:01Z", marker=marker, transition_plan_sha256=plan_sha)
        index = release_notes.validate_index(store.index)
        old_body = note(version=release_notes.LEGACY_RELEASE_VERSION, changed="Original legacy text")
        old_digest = release_notes.digest_bytes(old_body)
        old_path = release_notes.exact_object_path(release_notes.LEGACY_RELEASE_VERSION, "en", old_digest)
        store.objects[old_path] = (old_body, store.next_generation)
        store.next_generation += 1
        index["versions"].append({
            "product_version": release_notes.LEGACY_RELEASE_VERSION,
            "published_at": "2026-11-13T04:00:00Z",
            "locales": {"en": {"sha256": old_digest, "object_path": old_path}},
        })
        store.index = canonical(index)
        store.generation += 1
        before_bytes, before_generation, before_writes = store.index, store.generation, store.writes
        aliases_before = copy.deepcopy(index["legacy_aliases"])
        with self.assertRaisesRegex(release_notes.ReleaseNotesError, "legacy-alias-row-immutable"):
            release_notes.publish_package(
                store, {"en": note(version=release_notes.LEGACY_RELEASE_VERSION, changed="Unauthorized correction")},
                product_version=release_notes.LEGACY_RELEASE_VERSION, published_at="2026-11-16T01:00:00Z",
                marker=None, historical=True)
        self.assertEqual((store.index, store.generation, store.writes),
                         (before_bytes, before_generation, before_writes))

        exact = release_notes.publish_package(
            store, {"en": old_body}, product_version=release_notes.LEGACY_RELEASE_VERSION,
            published_at="2026-11-16T01:00:00Z", marker=None, historical=True)
        self.assertEqual(exact.index_generation, before_generation)
        self.assertEqual(store.index, before_bytes)

        canonical_version = release_notes.PUBLIC_RELEASE_VERSION
        original_time = next(row["published_at"] for row in index["versions"]
                             if row["product_version"] == canonical_version)
        release_notes.publish_package(
            store, {"en": note(version=canonical_version, changed="Canonical correction")},
            product_version=canonical_version, published_at="2026-11-16T02:00:00Z",
            marker=marker, allow_alias_creation=False)
        after = release_notes.validate_index(store.index)
        self.assertEqual(after["legacy_aliases"], aliases_before)
        self.assertEqual(next(row["published_at"] for row in after["versions"]
                              if row["product_version"] == release_notes.LEGACY_RELEASE_VERSION),
                         "2026-11-13T04:00:00Z")
        self.assertEqual(next(row["published_at"] for row in after["versions"]
                              if row["product_version"] == canonical_version), original_time)
        self.assertEqual(after["current_deployment"], marker)

    def test_current_marker_token_digest_uses_original_json_bytes(self):
        marker = normalized_marker(operation="deploy", product_version="1.0.0")
        token = json.dumps(marker, ensure_ascii=False, indent=2).encode("utf-8")
        index_bytes = b'{"schema_version":1,"versions":[],"current_deployment": ' + token + b'}'
        extracted = release_notes.current_deployment_token_bytes(index_bytes)
        self.assertEqual(extracted, token)
        previous = release_notes.previous_release_from_marker(marker, extracted)
        self.assertEqual(previous["marker_sha256"], release_notes.digest_bytes(token))
        self.assertNotEqual(previous["marker_sha256"], release_notes.digest_bytes(canonical(marker)))

    def test_index_validation_rejects_both_mixed_marker_schema_pairs(self):
        _, schema2_marker, _, _ = initial_schema2_migration_fixture()
        schema1_marker = normalized_marker(operation="deploy", product_version="1.0.0")
        mixed_v1_index = canonical({"schema_version": 1, "versions": [], "current_deployment": schema2_marker})
        mixed_v2_index = canonical({"schema_version": release_notes.INDEX_SCHEMA_VERSION, "versions": [],
                                    "current_deployment": schema1_marker, "legacy_aliases": []})
        for data in (mixed_v1_index, mixed_v2_index):
            with self.subTest(index=data[:20]), self.assertRaisesRegex(
                    release_notes.ReleaseNotesError, "index-marker-schema-mismatch"):
                release_notes.validate_index(data)

    def test_http_reader_readback_requires_identity_and_exact_stored_content(self):
        from io import BytesIO

        expected_body = note(version=release_notes.PUBLIC_RELEASE_VERSION, changed="Canonical content")
        legacy_body = note(version=release_notes.LEGACY_RELEASE_VERSION, changed="Immutable legacy content")
        store, marker, plan_sha, _ = initial_schema2_migration_fixture()
        canonical_time = "2026-11-15T01:14:01Z"
        old_time = "2026-11-01T22:58:30+08:00"
        published = release_notes.publish_package(
            store, {"en": expected_body}, product_version=release_notes.PUBLIC_RELEASE_VERSION,
            published_at=canonical_time, marker=marker, transition_plan_sha256=plan_sha)
        index = release_notes.validate_index(store.index)
        old_digest = release_notes.digest_bytes(legacy_body)
        old_path = release_notes.exact_object_path(release_notes.LEGACY_RELEASE_VERSION, "en", old_digest)
        store.objects[old_path] = (legacy_body, store.next_generation)
        store.next_generation += 1
        index["versions"].append({
            "product_version": release_notes.LEGACY_RELEASE_VERSION, "published_at": old_time,
            "locales": {"en": {"sha256": old_digest, "object_path": old_path}},
        })
        store.index = canonical(index)
        store.generation += 1

        def public_index(*, fallback=False):
            row = None if fallback else next(row for row in index["versions"]
                                            if row["product_version"] == release_notes.PUBLIC_RELEASE_VERSION)
            return {
                "schema_version": 1, "environment": "dev", "runtime_product_version": marker["product_version"],
                "current_status": "missing" if fallback else "available",
                "current_product_version": release_notes.PUBLIC_RELEASE_VERSION,
                **({} if fallback else {"current": {"product_version": row["product_version"],
                                                     "published_at": row["published_at"],
                                                     "locales": ["en"]}}),
                "history": [],
                "component_details_status": "unavailable" if fallback else "complete",
                "current_components": [] if fallback else [{
                    "component": "bff", "product_version": marker["product_version"],
                    "commit": marker["commit"], "image_tag": marker["image_tag"],
                    "image_digest": marker["image_digest"], "revision": marker["revision"],
                }],
                "compatibility_aliases": [{"requested_version": release_notes.LEGACY_RELEASE_VERSION,
                                           "canonical_version": release_notes.PUBLIC_RELEASE_VERSION,
                                           "legacy_row_version": release_notes.LEGACY_RELEASE_VERSION}],
            }

        class Response:
            status = 200
            headers = {"Cache-Control": "no-store"}
            def __init__(self, body): self.stream = BytesIO(body)
            def read(self, size=-1): return self.stream.read(size)
            def __enter__(self): return self
            def __exit__(self, *_args): return False

        def dto(body, requested, canonical_version, *, published_at, alias, fallback, current):
            return {**json.loads(body), "published_at": published_at, "requested_locale": "en",
                    "locale_fallback": False, "is_current": current,
                    "requested_product_version": requested, "canonical_product_version": canonical_version,
                    "legacy_alias": alias, "legacy_fallback": fallback}

        def run(path_version, content, api_index, *, current_store=store):
            with TemporaryDirectory() as temporary, \
                    patch.object(release_notes_capture, "active_service_url", return_value="https://notes.invalid"), \
                    patch.object(release_notes_publish, "urlopen", side_effect=[
                        Response(canonical(api_index)), Response(canonical(content))]):
                return release_notes_publish.verify_reader_api(
                    Path(temporary), "dev", None, path_version, {"en": legacy_body}, historical=True,
                    store=current_store, alias_receipt=published.legacy_alias_receipt,
                    minimum_index_generation=published.index_generation)

        canonical_response = dto(expected_body, release_notes.PUBLIC_RELEASE_VERSION,
                                 release_notes.PUBLIC_RELEASE_VERSION, published_at=canonical_time,
                                 alias=False, fallback=False, current=True)
        verified = run(release_notes.PUBLIC_RELEASE_VERSION, canonical_response, public_index())
        self.assertEqual(verified["reader_requested_product_version"], release_notes.PUBLIC_RELEASE_VERSION)
        self.assertEqual(verified["reader_canonical_product_version"], release_notes.PUBLIC_RELEASE_VERSION)
        self.assertTrue(verified["reader_current_flags"]["en"])
        self.assertEqual(verified["reader_content_sha256"]["en"],
                         release_notes.digest_bytes(canonical(json.loads(expected_body))))

        # The retained 1.0.0 URL resolves to the canonical public 2026.11.4 body.
        alias_response = dto(expected_body, release_notes.LEGACY_RELEASE_VERSION,
                             release_notes.PUBLIC_RELEASE_VERSION, published_at=canonical_time,
                             alias=True, fallback=False, current=True)
        alias_readback = run(release_notes.LEGACY_RELEASE_VERSION, alias_response, public_index())
        self.assertEqual(alias_readback["reader_requested_product_version"], release_notes.LEGACY_RELEASE_VERSION)
        self.assertEqual(alias_readback["reader_canonical_product_version"], release_notes.PUBLIC_RELEASE_VERSION)
        self.assertTrue(alias_readback["reader_legacy_alias"])
        self.assertFalse(alias_readback["reader_legacy_fallback"]["en"])
        self.assertTrue(alias_readback["reader_current_flags"]["en"])

        # If canonical content is absent, only the exact immutable legacy row is a valid false-current fallback.
        fallback_index = copy.deepcopy(index)
        fallback_index["versions"] = [row for row in fallback_index["versions"]
                                      if row["product_version"] == release_notes.LEGACY_RELEASE_VERSION]
        fallback_store = MemoryNotesStore()
        fallback_store.index = canonical(fallback_index)
        fallback_store.generation = store.generation + 1
        fallback_store.objects = copy.deepcopy(store.objects)
        fallback_response = dto(legacy_body, release_notes.LEGACY_RELEASE_VERSION,
                                release_notes.PUBLIC_RELEASE_VERSION, published_at=old_time,
                                alias=True, fallback=True, current=False)
        fallback_api_index = public_index(fallback=True)
        # It is the actual store snapshot used by the readback, so generation stability is still enforced.
        fallback_api_index["history"] = []
        fallback_readback = run(release_notes.LEGACY_RELEASE_VERSION, fallback_response,
                                 fallback_api_index, current_store=fallback_store)
        self.assertTrue(fallback_readback["reader_legacy_fallback"]["en"])
        self.assertFalse(fallback_readback["reader_current_flags"]["en"])

        for corrupt in (
                {key: value for key, value in alias_response.items() if key != "requested_product_version"},
                {**alias_response, "entries": [{"title": "wrong"}]},
                {**alias_response, "canonical_product_version": "2026.11.3"},
                {**alias_response, "is_current": False},
                {**alias_response, "legacy_fallback": True},
        ):
            with self.subTest(corrupt=corrupt), self.assertRaisesRegex(
                    release_notes.ReleaseNotesError, "reader-content-readback-mismatch"):
                run(release_notes.LEGACY_RELEASE_VERSION, corrupt, public_index())

        bad_mapping = public_index()
        bad_mapping["compatibility_aliases"] = [{"requested_version": "1.0.0",
                                                 "canonical_version": "2026.11.5",
                                                 "legacy_row_version": "1.0.0"}]
        with self.assertRaisesRegex(release_notes.ReleaseNotesError, "reader-index-readback-invalid"):
            run(release_notes.LEGACY_RELEASE_VERSION, alias_response, bad_mapping)

    def test_first_create_race_with_different_digest_conflicts_and_preserves_winner(self):
        marker = self.normalize(operation="deploy", service_generation=44,
                                revision="rev-w", product_version="1.2.3")
        store = InterleavingNotesStore()
        store.active_marker = marker
        winner = note(changed="Editor B")
        store.contender = lambda current: release_notes.publish_package(
            current, {"en": winner}, product_version="1.2.3",
            published_at="2026-11-14T10:00:00Z", marker=marker)

        with self.assertRaises(release_notes.ReleaseNotesError) as rejected:
            release_notes.publish_package(store, {"en": note(changed="Editor A")}, product_version="1.2.3",
                published_at="2026-11-14T10:00:00Z", marker=marker)

        self.assertEqual((rejected.exception.stage, rejected.exception.safe_class),
                         ("index_update", "generation-conflict"))
        index = json.loads(store.index)
        self.assertEqual(len(index["versions"]), 1)
        row = index["versions"][0]
        winner_path = row["locales"]["en"]["object_path"]
        self.assertEqual(store.read_content(winner_path), winner)
        self.assertEqual(row["published_at"], "2026-11-14T10:00:00Z")
        self.assertEqual(index["current_deployment"], marker)
        self.assertEqual((store.generation, store.writes), (1, 1))

    def test_same_version_correction_race_conflicts_and_preserves_winner_t0_and_marker(self):
        marker = self.normalize(operation="deploy", service_generation=44,
                                revision="rev-w", product_version="1.2.3")
        store = InterleavingNotesStore()
        store.active_marker = marker
        t0 = "2026-11-14T10:00:00Z"
        release_notes.publish_package(store, {"en": note(changed="Initial")}, product_version="1.2.3",
            published_at=t0, marker=marker)
        winner = note(changed="Editor B")
        store.contender = lambda current: release_notes.publish_package(
            current, {"en": winner}, product_version="1.2.3",
            published_at="2026-11-15T10:00:00Z", marker=marker)

        with self.assertRaises(release_notes.ReleaseNotesError) as rejected:
            release_notes.publish_package(store, {"en": note(changed="Editor A")}, product_version="1.2.3",
                published_at="2026-11-15T10:00:00Z", marker=marker)

        self.assertEqual((rejected.exception.stage, rejected.exception.safe_class),
                         ("index_update", "generation-conflict"))
        index = json.loads(store.index)
        self.assertEqual(len(index["versions"]), 1)
        row = index["versions"][0]
        winner_path = row["locales"]["en"]["object_path"]
        self.assertEqual(store.read_content(winner_path), winner)
        self.assertEqual(row["published_at"], t0)
        self.assertEqual(index["current_deployment"], marker)
        self.assertEqual((store.generation, store.writes), (2, 2))

    def test_same_package_first_create_race_is_idempotent(self):
        marker = self.normalize(operation="deploy", service_generation=44,
                                revision="rev-w", product_version="1.2.3")
        store = InterleavingNotesStore()
        store.active_marker = marker
        body = note()
        t0 = "2026-11-14T10:00:00Z"
        store.contender = lambda current: release_notes.publish_package(
            current, {"en": body}, product_version="1.2.3", published_at=t0, marker=marker)

        replay = release_notes.publish_package(store, {"en": body}, product_version="1.2.3",
            published_at="2026-11-15T10:00:00Z", marker=marker)

        index = json.loads(store.index)
        self.assertEqual((replay.index_generation, store.generation, store.writes), (1, 1, 1))
        self.assertEqual(len(index["versions"]), 1)
        self.assertEqual(index["versions"][0]["published_at"], t0)
        self.assertEqual(store.read_content(index["versions"][0]["locales"]["en"]["object_path"]), body)
        self.assertEqual(index["current_deployment"], marker)

    def test_unrelated_version_commit_is_merged_after_same_version_correction_cas_retry(self):
        marker = self.normalize(operation="deploy", service_generation=44,
                                revision="rev-w", product_version="1.2.3")
        store = InterleavingNotesStore()
        store.active_marker = marker
        t0 = "2026-11-14T10:00:00Z"
        release_notes.publish_package(store, {"en": note(changed="Initial")}, product_version="1.2.3",
            published_at=t0, marker=marker)
        unrelated = note(version="2.0.0", changed="Historical import")
        store.contender = lambda current: release_notes.publish_package(
            current, {"en": unrelated}, product_version="2.0.0",
            published_at="2026-11-15T10:00:00Z", marker=None, historical=True)

        corrected = release_notes.publish_package(store, {"en": note(changed="Corrected")}, product_version="1.2.3",
            published_at="2026-11-15T12:00:00Z", marker=marker)

        index = json.loads(store.index)
        rows = {row["product_version"]: row for row in index["versions"]}
        corrected_row = rows["1.2.3"]
        unrelated_row = rows["2.0.0"]
        self.assertEqual((corrected.index_generation, store.generation, store.writes), (3, 3, 3))
        self.assertEqual(len(index["versions"]), 2)
        self.assertEqual(corrected_row["published_at"], t0)
        self.assertEqual(store.read_content(corrected_row["locales"]["en"]["object_path"]), note(changed="Corrected"))
        self.assertEqual(store.read_content(unrelated_row["locales"]["en"]["object_path"]), unrelated)
        self.assertEqual(index["current_deployment"], marker)

    def test_index_conflicts_remain_bounded_when_only_unrelated_rows_change(self):
        class AlwaysConflictingStore(MemoryNotesStore):
            def __init__(self):
                super().__init__()
                self.competitor_writes = 0

            def compare_and_swap_index(self, data, generation):
                index = json.loads(self.index) if self.index is not None else {"schema_version": 1, "versions": []}
                version = f"{self.competitor_writes + 2}.0.0"
                digest = f"{self.competitor_writes + 1:064x}"
                index["versions"].append({
                    "product_version": version,
                    "published_at": "2026-11-14T10:00:00Z",
                    "locales": {"en": {"sha256": digest,
                                         "object_path": release_notes.exact_object_path(version, "en", digest)}},
                })
                self.index = canonical(index)
                self.generation += 1
                self.writes += 1
                self.competitor_writes += 1
                raise GenerationConflict()

        store = AlwaysConflictingStore()
        with self.assertRaises(release_notes.ReleaseNotesError) as rejected:
            release_notes.publish_package(store, {"en": note()}, product_version="1.2.3",
                published_at="2026-11-14T10:00:00Z", marker=None, historical=True, max_cas_attempts=2)

        self.assertEqual((rejected.exception.stage, rejected.exception.safe_class),
                         ("index_update", "index-conflict-exhausted"))
        index = json.loads(store.index)
        self.assertEqual({row["product_version"] for row in index["versions"]}, {"2.0.0", "3.0.0"})
        self.assertEqual(store.competitor_writes, 2)

    def test_resend_caller_records_same_version_correction_conflict_for_owner(self):
        fixture = operation_fixture(operation="release", product_version="1.2.3")
        plan_bytes, state_bytes, result_bytes, sidecar_bytes, receipt_bytes, _ = fixture
        marker, _ = release_notes.normalize_operation(
            *fixture[:4], environment="dev", workflow_operation="release", run_id="701", run_attempt=1,
            service="svc", receipt_bytes=receipt_bytes)
        store = InterleavingNotesStore()
        store.active_marker = marker
        t0 = "2026-11-14T10:00:00Z"
        release_notes.publish_package(store, {"en": note(changed="Initial")}, product_version="1.2.3",
            published_at=t0, marker=marker)
        winner = note(changed="Editor B")
        store.contender = lambda current: release_notes.publish_package(
            current, {"en": winner}, product_version="1.2.3", published_at="2026-11-15T10:00:00Z",
            marker=marker)

        with TemporaryDirectory() as temporary:
            directory = Path(temporary)
            plan_path = directory / "plan.json"
            plan_path.write_bytes(plan_bytes)
            (directory / "state.json").write_bytes(state_bytes)
            (directory / "result.json").write_bytes(result_bytes)
            (directory / "receipts").mkdir()
            (directory / "receipts" / "bff.json").write_bytes(receipt_bytes)
            package = directory / "package"
            package.mkdir()
            body = note(changed="Editor A")
            manifest = {
                "schema_version": 1, "product": "llm-wiki-cloud", "product_version": "1.2.3", "environment": "dev",
                "release_plan_sha256": release_notes.digest_bytes(plan_bytes),
                "release_note_snapshot_ref": "LWC-A-57:4-123",
                "release_note_snapshot_sha256": "sha256:" + "a" * 64,
                "delivered_scope_evidence_sha256": "sha256:" + "b" * 64,
                "files": {"en": {"path": "en.json", "sha256": release_notes.digest_bytes(body)}},
                "original_published_at": t0,
                "historical_source_record_ref": "LWC-A-57:4-123",
                "historical_source_record_sha256": "sha256:" + "d" * 64,
                "imported_at": "2026-11-15T00:00:00Z",
            }
            (package / "release-notes-manifest.json").write_bytes(canonical(manifest))
            (package / "en.json").write_bytes(body)
            package_artifact_name = release_notes.historical_package_artifact_name(
                "1.2.3", "release", "9001", "8002", "1")
            args = release_notes_publish.parser().parse_args([
                "--action", "resend", "--directory", str(directory), "--package-dir", str(package),
                "--environment", "dev", "--bucket", "unused", "--artifact-id", "8001",
                "--artifact-digest", "sha256:" + "c" * 64,
                "--artifact-metadata-digest", "sha256:" + "c" * 64,
                "--artifact-name", package_artifact_name, "--package-run-id", "8002",
                "--package-run-attempt", "1",
                "--plan", str(plan_path), "--operation", "release", "--run-id", "701",
                "--run-attempt", "1", "--result-artifact-id", "9001", "--product-status", "released",
            ])
            with patch.object(release_notes_publish, "GCSCLIStore", return_value=store), \
                    patch.object(release_notes_capture, "active_marker_matches", return_value=True):
                with self.assertRaises(release_notes.ReleaseNotesError) as rejected:
                    release_notes_publish.execute(args)

            self.assertEqual((rejected.exception.stage, rejected.exception.safe_class),
                             ("index_update", "generation-conflict"))
            index = json.loads(store.index)
            row = index["versions"][0]
            self.assertEqual(len(index["versions"]), 1)
            self.assertEqual(row["published_at"], t0)
            self.assertEqual(index["current_deployment"], marker)
            self.assertEqual(store.read_content(row["locales"]["en"]["object_path"]), winner)
            stages = json.loads((directory / "release-notes-evidence.json").read_text())["stages"]
            failure = stages[-1]
            self.assertEqual((failure["stage"], failure["safe_class"], failure["status"]),
                             ("index_update", "generation-conflict", "failed"))
            self.assertEqual(failure["next_actions"], ["retry", "skip", "stop"])
            self.assertTrue(failure["owner_choice_required"])
            self.assertFalse(failure["automatic_skip"])

    def test_historical_artifact_name_requires_complete_historical_manifest_before_store(self):
        fixture = operation_fixture(operation="release", product_version="1.2.3")
        plan_bytes, state_bytes, result_bytes, _, receipt_bytes, _ = fixture
        with TemporaryDirectory() as temporary:
            directory = Path(temporary)
            plan_path = directory / "plan.json"
            plan_path.write_bytes(plan_bytes)
            (directory / "state.json").write_bytes(state_bytes)
            (directory / "result.json").write_bytes(result_bytes)
            (directory / "receipts").mkdir()
            (directory / "receipts" / "bff.json").write_bytes(receipt_bytes)
            package = directory / "package"
            package.mkdir()
            body = note()
            manifest = {
                "schema_version": 1, "product": "llm-wiki-cloud", "product_version": "1.2.3", "environment": "dev",
                "release_plan_sha256": release_notes.digest_bytes(plan_bytes),
                "release_note_snapshot_ref": "LWC-A-57:4-123",
                "release_note_snapshot_sha256": "sha256:" + "a" * 64,
                "delivered_scope_evidence_sha256": "sha256:" + "b" * 64,
                "files": {"en": {"path": "en.json", "sha256": release_notes.digest_bytes(body)}},
            }
            (package / "release-notes-manifest.json").write_bytes(canonical(manifest))
            (package / "en.json").write_bytes(body)
            artifact_name = release_notes.historical_package_artifact_name(
                "1.2.3", "release", "9001", "8002", "1")
            args = release_notes_publish.parser().parse_args([
                "--action", "resend", "--directory", str(directory), "--package-dir", str(package),
                "--environment", "dev", "--bucket", "unused", "--artifact-id", "8001",
                "--artifact-digest", "sha256:" + "c" * 64,
                "--artifact-metadata-digest", "sha256:" + "c" * 64,
                "--artifact-name", artifact_name, "--package-run-id", "8002",
                "--package-run-attempt", "1", "--plan", str(plan_path),
                "--operation", "release", "--run-id", "701", "--run-attempt", "1",
                "--result-artifact-id", "9001", "--product-status", "released",
            ])
            with patch.object(release_notes_publish, "GCSCLIStore",
                              side_effect=AssertionError("invalid history package must fail before storage")):
                with self.assertRaisesRegex(release_notes.ReleaseNotesError,
                                            "historical-provenance-required"):
                    release_notes_publish.execute(args)

    def test_marker_only_skip_updates_currentness_without_content_or_timestamp(self):
        marker = self.normalize()
        store = MemoryNotesStore()
        store.active_marker = marker
        result = release_notes.publish_marker_only(store, marker)
        index = json.loads(store.index)
        self.assertEqual(index["versions"], [])
        self.assertEqual(index["current_deployment"], marker)
        self.assertEqual(result.index_generation, 1)
        self.assertEqual(release_notes.failure_disposition("index_update", "released")["allowed_next_actions"], ["retry", "skip", "stop"])

    def test_invalid_locale_prevents_all_storage_writes(self):
        marker = self.normalize()
        store = MemoryNotesStore()
        store.active_marker = marker
        with self.assertRaises(release_notes.ReleaseNotesError):
            release_notes.publish_package(store, {"en": note(), "zh-TW": b"not-json"}, product_version="1.2.3",
                published_at="2026-11-14T10:00:00Z", marker=marker)
        self.assertEqual(store.objects, {})
        self.assertEqual(store.writes, 0)

    def test_upload_content_validates_every_locale_before_first_write(self):
        store = MemoryNotesStore()
        with self.assertRaises(release_notes.ReleaseNotesError):
            release_notes.upload_content(store, {"en": note(), "zh-TW": b"{\"x\":1,\"x\":2}"}, product_version="1.2.3")
        self.assertEqual(store.objects, {})

    def test_private_package_manifest_is_pinned_closed_and_historical_time_is_explicit(self):
        plan_bytes = canonical(plan_fixture())
        package_body = note()
        artifact_digest = "sha256:" + "c" * 64
        with TemporaryDirectory() as temp:
            root = Path(temp)
            (root / "en.json").write_bytes(package_body)
            manifest = {
                "schema_version": 1, "product": "llm-wiki-cloud", "product_version": "1.2.3", "environment": "dev",
                "release_plan_sha256": release_notes.digest_bytes(plan_bytes), "release_note_snapshot_ref": "LWC-A-57:4-123",
                "release_note_snapshot_sha256": "sha256:" + "a" * 64,
                "delivered_scope_evidence_sha256": "sha256:" + "b" * 64,
                "files": {"en": {"path": "en.json", "sha256": release_notes.digest_bytes(package_body)}},
            }
            manifest_path = root / "release-notes-manifest.json"
            manifest_path.write_bytes(canonical(manifest))
            version, files, _ = release_notes.validate_package(root, expected_environment="dev",
                expected_artifact_digest=artifact_digest, artifact_metadata_digest=artifact_digest, plan_bytes=plan_bytes)
            self.assertEqual(version, "1.2.3")
            self.assertEqual(files, {"en": package_body})
            manifest.update({"original_published_at": "2026-10-02T00:00:00Z",
                             "historical_source_record_ref": "LWC-A-57:4-5390",
                             "historical_source_record_sha256": "sha256:" + "d" * 64,
                             "imported_at": "2026-11-14T10:00:00Z"})
            manifest_path.write_bytes(canonical(manifest))
            version, _, validated = release_notes.validate_package(root, expected_environment="dev",
                expected_artifact_digest=artifact_digest, artifact_metadata_digest=artifact_digest, plan_bytes=plan_bytes)
            self.assertEqual(validated["original_published_at"], "2026-10-02T00:00:00Z")
            manifest["unexpected"] = True
            manifest_path.write_bytes(canonical(manifest))
            with self.assertRaisesRegex(release_notes.ReleaseNotesError, "unknown-fields"):
                release_notes.validate_package(root, expected_environment="dev", expected_artifact_digest=artifact_digest,
                    artifact_metadata_digest=artifact_digest, plan_bytes=plan_bytes)

    def test_gcs_cli_index_snapshot_and_conditional_cas_contract(self):
        store = release_notes_publish.GCSCLIStore("llm-wiki-data-dev")
        responses = [
            type("Result", (), {"returncode": 0, "stdout": b'{"generation":"8"}', "stderr": b""})(),
            type("Result", (), {"returncode": 0, "stdout": b'{"schema_version":1,"versions":[]}', "stderr": b""})(),
            type("Result", (), {"returncode": 0, "stdout": b'{"generation":"8"}', "stderr": b""})(),
        ]
        with patch.object(release_notes_publish.subprocess, "run", side_effect=responses) as run:
            body, generation = store.read_index()
        self.assertEqual(body, b'{"schema_version":1,"versions":[]}')
        self.assertEqual(generation, 8)
        self.assertEqual(run.call_count, 3)

        responses = [type("Result", (), {"returncode": 0, "stdout": b"", "stderr": b""})(),
                     type("Result", (), {"returncode": 0, "stdout": b'{"generation":"9"}', "stderr": b""})()]
        with patch.object(release_notes_publish.subprocess, "run", side_effect=responses) as run:
            self.assertEqual(store.compare_and_swap_index(b'{"schema_version":1,"versions":[]}', 8), 9)
        self.assertIn("--if-generation-match=8", run.call_args_list[0].args[0])

    def test_gcs_cli_cas_reports_conflict_only_after_changed_generation(self):
        store = release_notes_publish.GCSCLIStore("llm-wiki-data-dev")
        failed = __import__("subprocess").CalledProcessError(1, ["gcloud", "storage", "cp"])
        changed = type("Result", (), {"returncode": 0, "stdout": b'{"generation":"10"}', "stderr": b""})()
        with patch.object(release_notes_publish.subprocess, "run", side_effect=[failed, changed]):
            with self.assertRaises(release_notes_publish.GenerationConflict):
                store.compare_and_swap_index(b'{"schema_version":1,"versions":[]}', 9)

    def test_historical_resend_adds_history_and_preserves_newer_current_marker(self):
        current = self.normalize(operation="deploy", service_generation=44, revision="rev-w", product_version="2.0.0")
        old = self.normalize(service_generation=43, product_version="1.2.3")
        store = MemoryNotesStore()
        store.active_marker = current
        release_notes.publish_marker_only(store, current)
        result = release_notes.publish_package(store, {"en": note()}, product_version="1.2.3",
            published_at="2026-10-02T00:00:00Z", marker=None, historical=True)
        index = json.loads(store.index)
        self.assertEqual(index["current_deployment"], current)
        self.assertEqual(index["versions"][0]["product_version"], "1.2.3")
        self.assertEqual(result.index_generation, 2)
        self.assertNotEqual(old, current)

    def test_first_resend_without_capture_sidecar_uses_retained_publication_time_history_only(self):
        fixture = operation_fixture(operation="release")
        plan_bytes, state_bytes, result_bytes, _, receipt_bytes, _ = fixture
        with TemporaryDirectory() as temp:
            directory = Path(temp)
            plan_path = directory / "plan.json"
            plan_path.write_bytes(plan_bytes)
            (directory / "state.json").write_bytes(state_bytes)
            (directory / "result.json").write_bytes(result_bytes)
            (directory / "receipts").mkdir()
            (directory / "receipts" / "bff.json").write_bytes(receipt_bytes)
            package = directory / "package"
            package.mkdir()
            body = note()
            manifest = {
                "schema_version": 1, "product": "llm-wiki-cloud", "product_version": "1.2.3", "environment": "dev",
                "release_plan_sha256": release_notes.digest_bytes(plan_bytes), "release_note_snapshot_ref": "LWC-A-57:4-123",
                "release_note_snapshot_sha256": "sha256:" + "a" * 64,
                "delivered_scope_evidence_sha256": "sha256:" + "b" * 64,
                "files": {"en": {"path": "en.json", "sha256": release_notes.digest_bytes(body)}},
            }
            (package / "release-notes-manifest.json").write_bytes(canonical(manifest))
            (package / "en.json").write_bytes(body)

            timestamp_args = release_notes_publish.parser().parse_args([
                "--action", "record-publication", "--directory", str(directory), "--environment", "dev",
                "--bucket", "unused", "--plan", str(plan_path), "--operation", "release",
                "--run-id", "701", "--run-attempt", "1", "--product-status", "released",
                "--published-at", "2026-11-14T10:00:00Z",
            ])
            recorded = release_notes_publish.execute(timestamp_args)
            self.assertEqual(recorded["status"], "verified")
            publication_path = directory / "release-notes-publication.json"
            publication_bytes = publication_path.read_bytes()

            capture_attempt = run_capture_cli_with_service_url_failure(
                directory, operation="release", action="run", artifact_id="8001")
            self.assertEqual(capture_attempt.returncode, 1, capture_attempt.stderr)
            capture_failure = json.loads(capture_attempt.stdout)
            self.assertEqual(capture_failure, {
                "stage": "index_update", "safe_class": "service-url-unavailable", "evidence_recorded": True,
            })
            retained_after_capture = json.loads((directory / "release-notes-evidence.json").read_text())["stages"]
            failure_record = retained_after_capture[-1]
            self.assertEqual(failure_record["action"], "capture")
            self.assertEqual(failure_record["status"], "failed")
            self.assertEqual(failure_record["product_status"], "released")
            self.assertEqual(failure_record["release_notes_action"], "run")
            self.assertEqual(failure_record["safe_class"], "service-url-unavailable")
            self.assertEqual(failure_record["published_at"], "2026-11-14T10:00:00Z")
            self.assertEqual(failure_record["publication_evidence_sha256"],
                             release_notes.digest_bytes(publication_bytes))
            self.assertEqual(failure_record["next_actions"], ["retry", "skip", "stop"])
            self.assertTrue(failure_record["owner_choice_required"])
            self.assertFalse(failure_record["automatic_skip"])
            self.assertFalse((directory / "release-notes-operation.json").exists())

            current = normalized_marker(operation="deploy", service_generation=44,
                                        revision="rev-w", product_version="9.0.0")
            store = MemoryNotesStore()
            store.active_marker = current
            release_notes.publish_marker_only(store, current)
            resend_args = release_notes_publish.parser().parse_args([
                "--action", "resend", "--directory", str(directory), "--package-dir", str(package),
                "--environment", "dev", "--bucket", "unused", "--artifact-id", "8001",
                "--artifact-digest", "sha256:" + "c" * 64,
                "--artifact-metadata-digest", "sha256:" + "c" * 64,
                "--plan", str(plan_path), "--operation", "release", "--run-id", "701", "--run-attempt", "1",
                "--result-artifact-id", "9001", "--product-status", "released",
                "--published-at", "2026-11-15T10:00:00Z",
            ])
            expected_reader_content = json.loads(body)
            with patch.object(release_notes_publish, "GCSCLIStore", return_value=store), \
                    patch.object(release_notes_publish, "verify_reader_api", return_value={
                        "reader_current_status": "unavailable",
                        "reader_current_product_version": None,
                        "reader_current_flags": {},
                        "reader_content_sha256": {"en": release_notes.digest_bytes(canonical(expected_reader_content))},
                    }), \
                    patch.object(release_notes_capture, "capture", side_effect=AssertionError("must not synthesize a readback")):
                evidence = release_notes_publish.execute(resend_args)

            index = json.loads(store.index)
            self.assertEqual(index["current_deployment"], current)
            self.assertEqual(len(index["versions"]), 1)
            self.assertEqual(index["versions"][0]["product_version"], "1.2.3")
            self.assertEqual(index["versions"][0]["published_at"], "2026-11-14T10:00:00Z")
            self.assertEqual(evidence["published_at_source"], "retained-release-operation")
            self.assertEqual(evidence["currentness"], "unavailable")
            self.assertEqual(evidence["publication_evidence_sha256"], release_notes.digest_bytes(publication_bytes))
            self.assertIsNone(evidence["operation_sidecar_sha256"])
            self.assertFalse((directory / "release-notes-operation.json").exists())
            retained_after_resend = json.loads((directory / "release-notes-evidence.json").read_text())["stages"]
            self.assertEqual([item["action"] for item in retained_after_resend],
                             ["record-publication", "capture", "resend"])
            self.assertEqual(retained_after_resend[1]["published_at"], "2026-11-14T10:00:00Z")
            self.assertEqual(retained_after_resend[1]["status"], "failed")
            self.assertEqual(retained_after_resend[2]["published_at"], "2026-11-14T10:00:00Z")

    def test_capture_failure_records_skip_and_marker_contexts(self):
        for operation, action in (("release", "skip"), ("deploy", "marker")):
            with self.subTest(operation=operation, action=action), TemporaryDirectory() as temp:
                directory = Path(temp)
                fixture = operation_fixture(operation=operation)
                for name, data in zip(("plan.json", "state.json", "result.json"), fixture[:3]):
                    (directory / name).write_bytes(data)
                (directory / "receipts").mkdir()
                (directory / "receipts" / "bff.json").write_bytes(fixture[4])

                capture_attempt = run_capture_cli_with_service_url_failure(
                    directory, operation=operation, action=action)
                self.assertEqual(capture_attempt.returncode, 1, capture_attempt.stderr)
                output = json.loads(capture_attempt.stdout)
                self.assertEqual(output["safe_class"], "service-url-unavailable")
                self.assertTrue(output["evidence_recorded"])
                evidence = json.loads((directory / "release-notes-evidence.json").read_text())["stages"]
                self.assertEqual(len(evidence), 1)
                self.assertEqual(evidence[0]["action"], "capture")
                self.assertEqual(evidence[0]["release_notes_action"], action)
                self.assertEqual(evidence[0]["product_status"], "released")
                self.assertEqual(evidence[0]["next_actions"], ["retry", "skip", "stop"])
                self.assertIsNone(evidence[0].get("published_at"))

    def test_resend_without_operation_readback_or_original_time_never_fabricates_publication(self):
        fixture = operation_fixture(operation="release")
        plan_bytes, state_bytes, result_bytes, _, receipt_bytes, _ = fixture
        with TemporaryDirectory() as temp:
            directory = Path(temp)
            plan_path = directory / "plan.json"
            plan_path.write_bytes(plan_bytes)
            (directory / "state.json").write_bytes(state_bytes)
            (directory / "result.json").write_bytes(result_bytes)
            (directory / "receipts").mkdir()
            (directory / "receipts" / "bff.json").write_bytes(receipt_bytes)
            package = directory / "package"
            package.mkdir()
            body = note()
            manifest = {
                "schema_version": 1, "product": "llm-wiki-cloud", "product_version": "1.2.3", "environment": "dev",
                "release_plan_sha256": release_notes.digest_bytes(plan_bytes), "release_note_snapshot_ref": "LWC-A-57:4-123",
                "release_note_snapshot_sha256": "sha256:" + "a" * 64,
                "delivered_scope_evidence_sha256": "sha256:" + "b" * 64,
                "files": {"en": {"path": "en.json", "sha256": release_notes.digest_bytes(body)}},
            }
            (package / "release-notes-manifest.json").write_bytes(canonical(manifest))
            (package / "en.json").write_bytes(body)
            store = MemoryNotesStore()
            args = release_notes_publish.parser().parse_args([
                "--action", "resend", "--directory", str(directory), "--package-dir", str(package),
                "--environment", "dev", "--bucket", "unused", "--artifact-id", "8001",
                "--artifact-digest", "sha256:" + "c" * 64,
                "--artifact-metadata-digest", "sha256:" + "c" * 64,
                "--plan", str(plan_path), "--operation", "release", "--run-id", "701", "--run-attempt", "1",
                "--result-artifact-id", "9001", "--product-status", "released",
            ])
            with patch.object(release_notes_publish, "GCSCLIStore", return_value=store), \
                    self.assertRaisesRegex(release_notes.ReleaseNotesError, "retained-publication-time-unavailable"):
                release_notes_publish.execute(args)
            self.assertEqual(store.objects, {})
            self.assertEqual(store.writes, 0)

    def test_stage_evidence_appends_attempts_without_overwriting_prior_failure(self):
        with TemporaryDirectory() as temp:
            directory = Path(temp)
            release_notes_publish.write_evidence(directory, {
                "schema_version": 1, "action": "publish", "stage": "index_update", "status": "failed",
            })
            release_notes_publish.write_evidence(directory, {
                "schema_version": 1, "action": "publish", "stage": "index_update", "status": "verified",
            })
            evidence = json.loads((directory / "release-notes-evidence.json").read_text())
        self.assertEqual([item["status"] for item in evidence["stages"]], ["failed", "verified"])
        self.assertEqual([item["event_sequence"] for item in evidence["stages"]], [1, 2])
        self.assertEqual([item["stage_attempt"] for item in evidence["stages"]], [1, 2])
        self.assertTrue(all(item["recorded_at"].endswith("Z") for item in evidence["stages"]))

    def test_post_release_skip_and_stop_are_recorded_without_cloud_access(self):
        with TemporaryDirectory() as temp:
            directory = Path(temp)
            plan_path = directory / "plan.json"
            plan, state, result, _, receipt, _ = operation_fixture()
            plan_path.write_bytes(plan)
            (directory / "state.json").write_bytes(state)
            (directory / "result.json").write_bytes(result)
            (directory / "receipts").mkdir()
            (directory / "receipts" / "bff.json").write_bytes(receipt)
            for action, expected_status in (("skip-after-release", "skipped"), ("stop", "stopped")):
                args = release_notes_publish.parser().parse_args([
                    "--action", action, "--directory", str(directory), "--environment", "dev",
                    "--bucket", "unused", "--plan", str(plan_path), "--operation", "rollback",
                    "--run-id", "701", "--run-attempt", "1", "--product-status", "released",
                    "--decision-actor", "owner", "--decision-time", "2026-11-14T10:00:00Z",
                ])
                with patch.object(release_notes_publish, "GCSCLIStore", side_effect=AssertionError("cloud access")):
                    evidence = release_notes_publish.execute(args)
                self.assertEqual(evidence["status"], expected_status)
            records = json.loads((directory / "release-notes-evidence.json").read_text())["stages"]
        self.assertEqual([item["action"] for item in records], ["skip-after-release", "stop"])
        self.assertTrue(records[0]["product_rollback"] is False)
        self.assertTrue(records[0]["product_redeploy"] is False)
        self.assertEqual(records[0]["notes_currentness"], "not_claimed")

    def test_resend_dispositions_require_an_offline_native_product_success_witness(self):
        fixture = operation_fixture()
        with TemporaryDirectory() as temp:
            directory = Path(temp)
            plan, state, result, _, receipt, _ = fixture
            (directory / "plan.json").write_bytes(plan)
            (directory / "state.json").write_bytes(state)
            (directory / "result.json").write_bytes(result)
            (directory / "receipts").mkdir()
            (directory / "receipts" / "bff.json").write_bytes(receipt)
            args = release_notes_publish.parser().parse_args([
                "--action", "verify-operation", "--directory", str(directory), "--environment", "dev",
                "--bucket", "unused", "--plan", str(directory / "plan.json"), "--operation", "rollback",
                "--run-id", "701", "--run-attempt", "1", "--result-artifact-id", "9001",
                "--product-status", "released",
            ])
            with patch.object(release_notes_publish, "GCSCLIStore", side_effect=AssertionError("cloud access")):
                evidence = release_notes_publish.execute(args)
            self.assertEqual(evidence["status"], "verified")
            self.assertEqual(evidence["product_status"], "released")
            self.assertEqual(evidence["result_artifact_id"], "9001")
            failed_result = json.loads(result)
            failed_result["status"] = "failed"
            (directory / "result.json").write_bytes(canonical(failed_result))
            with self.assertRaisesRegex(release_notes.ReleaseNotesError, "native-operation-unsuccessful"):
                release_notes.validate_native_operation(plan, state, canonical(failed_result), receipt,
                    environment="dev", workflow_operation="rollback", run_id="701", run_attempt=1)


if __name__ == "__main__":
    unittest.main()
