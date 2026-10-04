#!/usr/bin/env python3
"""Offline contract tests for the Actions-owned develop-to-main status."""

import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

import yaml


ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = ROOT / ".github/workflows/deploy-dev.yml"


class MainFastForwardEligibilityTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.workflow = yaml.safe_load(WORKFLOW.read_text())
        cls.job = cls.workflow["jobs"]["main-fast-forward-eligible"]
        cls.steps = {step.get("name"): step for step in cls.job["steps"]}

    def test_registered_workflow_restores_required_status_from_current_result(self):
        self.assertEqual(self.job["name"], "main-fast-forward-eligible")
        self.assertEqual(self.job["needs"], "release")
        self.assertIn("inputs.operation != 'readback'", self.job["if"])
        self.assertEqual(self.job["permissions"], {
            "contents": "read", "actions": "read", "statuses": "write",
        })
        download = self.steps["Download this DEV attempt result"]
        self.assertEqual(download["if"], "needs.release.result == 'success'")
        self.assertEqual(download["with"]["name"],
                         "lwc-result-development-${{ github.run_id }}-${{ github.run_attempt }}")
        self.assertIn("actions/download-artifact@", download["uses"])
        checkout = self.steps["Checkout exact candidate"]
        self.assertEqual(checkout["with"]["ref"], "${{ github.sha }}")
        self.assertFalse(checkout["with"]["persist-credentials"])
        validation = self.steps["Validate formal DEV result and exact main fast-forward eligibility"]
        self.assertEqual(validation["if"], "always()")
        self.assertIn("${{ needs.release.result }}", str(validation["env"]))
        self.assertIn("${{ inputs.operation", str(validation["env"]))
        self.assertEqual(self.steps["Publish eligibility failure status"]["if"],
                         "${{ failure() || cancelled() }}")

    def _fixture(self, root, *, candidate, source, operation="release", status="success"):
        result_dir = root / "dev-result"
        result_dir.mkdir()
        plan = {
            "id": "d" * 64,
            "source": source,
            "tag": "dev-lwc-358-test",
            "branch": "develop",
            "selected": ["auth", "frontend"],
            "normalized": {"environment": "development"},
            # This is provenance, not a requirement that source and current executor match.
            "executor_sha": "a" * 40,
        }
        state = {
            "plan": plan["id"], "status": status, "sequence": 9,
            "executor_sha": candidate,
        }
        result = {
            "release": plan["tag"], "attempt": plan["id"], "stage": status,
            "status": status, "reason": "completed" if status == "success" else "unknown",
            "expected": {"source": source}, "last_verified_checkpoint": state["sequence"],
        }
        for name, payload in (("plan.json", plan), ("state.json", state), ("result.json", result)):
            (result_dir / name).write_text(json.dumps(payload))
        return result_dir

    def _run_job_contract(self, *, operation="release", deploy_result="success",
                          result_status="success", source=None, develop_head=None,
                          ancestor=True, artifact_available=True, candidate=None):
        if operation == "readback":
            return None, []
        candidate = candidate or "c" * 40
        source = source or candidate
        develop_head = develop_head or candidate
        with tempfile.TemporaryDirectory(prefix="lwc-main-eligible-") as directory:
            root = Path(directory)
            bin_dir = root / "bin"
            bin_dir.mkdir()
            result_dir = self._fixture(root, candidate=candidate, source=source,
                                       operation=operation, status=result_status)
            status_log = root / "statuses.log"
            (bin_dir / "gh").write_text(
                "#!/usr/bin/env bash\nset -eu\n"
                "state='' context=''\n"
                "for arg in \"$@\"; do case \"$arg\" in state=*) state=${arg#state=};; context=*) context=${arg#context=};; esac; done\n"
                "case \"$*\" in *git/ref/heads/develop*) printf '%s\\n' \"$FAKE_DEVELOP_SHA\"; exit 0;; *git/ref/heads/main*) printf '%s\\n' \"$FAKE_MAIN_SHA\"; exit 0;; esac\n"
                "[[ $context == main-fast-forward-eligible ]]\n"
                f"printf '%s\\n' \"$state\" >> {status_log}\n"
            )
            (bin_dir / "git").write_text(
                "#!/usr/bin/env bash\nset -eu\n"
                "case \"$1\" in\n"
                f"  rev-parse) if [[ \"${{!#}}\" == refs/remotes/origin/main ]]; then printf '%s\\n' \"$FAKE_MAIN_SHA\"; else printf '%s\\n' \"$GITHUB_SHA\"; fi ;;\n"
                "  merge-base) [[ $FAKE_ANCESTOR == true ]] ;;\n"
                "  *) exit 92 ;;\n"
                "esac\n"
            )
            for path in (bin_dir / "gh", bin_dir / "git"):
                path.chmod(0o755)

            env = {
                **os.environ,
                "PATH": f"{bin_dir}:{os.environ['PATH']}",
                "GITHUB_REPOSITORY": "Rayer/llm-wiki-cloud",
                "GITHUB_SHA": candidate,
                "GITHUB_TOKEN": "fixture-token",
                "GH_TOKEN": "fixture-token",
                "DEPLOY_RESULT": deploy_result,
                "OPERATION": operation,
                "EXPECTED_SOURCE": source,
                "RELEASE_TAG": "dev-lwc-358-test",
                "COMPONENTS": "auth,frontend",
                "RESULT_DIR": str(result_dir),
                "RESULT_DOWNLOAD_OUTCOME": "success" if artifact_available else "skipped",
                "CHECKOUT_OUTCOME": "success" if deploy_result == "success" else "skipped",
                "PENDING_OUTCOME": "success",
                "FAKE_MAIN_SHA": "b" * 40,
                "FAKE_DEVELOP_SHA": develop_head,
                "FAKE_ANCESTOR": "true" if ancestor else "false",
            }
            pending = self.steps["Publish pending eligibility status"]["run"]
            validation = self.steps[
                "Validate formal DEV result and exact main fast-forward eligibility"
            ]["run"]
            failure = self.steps["Publish eligibility failure status"]["run"]
            pending_result = subprocess.run(["bash", "-c", pending], env=env,
                                             text=True, capture_output=True, cwd=root)
            self.assertEqual(pending_result.returncode, 0, pending_result.stderr)
            checked = subprocess.run(["bash", "-c", validation], env=env,
                                     text=True, capture_output=True, cwd=root)
            if checked.returncode != 0:
                published_failure = subprocess.run(["bash", "-c", failure], env=env,
                                                   text=True, capture_output=True, cwd=root)
                self.assertEqual(published_failure.returncode, 0, published_failure.stderr)
            statuses = status_log.read_text().splitlines()
            return checked, statuses

    def test_successful_dev_result_publishes_required_status(self):
        checked, statuses = self._run_job_contract()
        self.assertEqual(checked.returncode, 0, checked.stdout + checked.stderr)
        self.assertEqual(statuses, ["pending", "success"])

    def test_retained_source_with_current_executor_is_eligible(self):
        # Matches the accepted recovery shape: source 173d290, executor/head ce6fb32.
        source = "173d290" + "0" * 33
        candidate = "ce6fb32" + "0" * 33
        checked, statuses = self._run_job_contract(
            operation="deploy", source=source, candidate=candidate)
        self.assertEqual(checked.returncode, 0, checked.stdout + checked.stderr)
        self.assertEqual(statuses, ["pending", "success"])

    def test_readback_does_not_publish_or_overwrite_eligibility_status(self):
        self.assertIn("inputs.operation != 'readback'", self.job["if"])
        checked, statuses = self._run_job_contract(operation="readback")
        self.assertIsNone(checked)
        self.assertEqual(statuses, [])

    def test_failed_or_unknown_formal_result_does_not_publish_success(self):
        for deploy_result, status in (("failure", "failed"), ("success", "unknown")):
            with self.subTest(deploy_result=deploy_result, status=status):
                checked, statuses = self._run_job_contract(
                    deploy_result=deploy_result, result_status=status,
                    artifact_available=deploy_result == "success")
                self.assertNotEqual(checked.returncode, 0)
                self.assertEqual(statuses, ["pending", "failure"])

    def test_changed_develop_head_or_nonancestor_main_does_not_publish_success(self):
        for develop_head, ancestor in (("e" * 40, True), ("c" * 40, False)):
            with self.subTest(develop_head=develop_head, ancestor=ancestor):
                checked, statuses = self._run_job_contract(
                    develop_head=develop_head, ancestor=ancestor)
                self.assertNotEqual(checked.returncode, 0)
                self.assertEqual(statuses, ["pending", "failure"])


if __name__ == "__main__":
    unittest.main()
