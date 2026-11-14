import json
import sys
import unittest
from pathlib import Path
from tempfile import TemporaryDirectory
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
sys.path.insert(0, str(Path(__file__).resolve().parent))
import release_notes_capture
from test_release_notes import V_IMAGE, operation_fixture


class ReleaseNotesOperationCaptureTests(unittest.TestCase):
    def setUp(self):
        self.temp = TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name)
        self.fixture = operation_fixture()
        for name, data in zip(("plan.json", "state.json", "result.json"), self.fixture[:3]):
            (self.directory / name).write_bytes(data)
        (self.directory / "receipts").mkdir()
        (self.directory / "receipts" / "bff.json").write_bytes(self.fixture[4])
        self.original_sidecar = self.fixture[5]

    def service(self, generation=43):
        return {"metadata": {"name": "svc", "uid": "uid-1", "generation": generation},
                "status": {"traffic": [{"revisionName": "rev-v", "percent": 100}], "url": "https://svc.invalid"}}

    def revision(self):
        return {"metadata": {"name": "rev-v"}, "status": {
            "conditions": [{"type": "Ready", "status": "True"}], "imageDigest": V_IMAGE},
            "spec": {"containers": [{"image": V_IMAGE}]}}

    def test_exact_native_rollback_artifact_captures_w_provenance_and_v_runtime_once(self):
        responses = [self.service(), self.revision()]
        build = self.original_sidecar["build_info"]
        with patch.object(release_notes_capture, "provider_json", side_effect=responses), \
                patch.object(release_notes_capture, "build_info", return_value=build):
            marker, sidecar_bytes = release_notes_capture.capture(self.directory, "rollback", "701", 1)
        sidecar = json.loads(sidecar_bytes)
        self.assertEqual(sidecar["operation"], "rollback")
        self.assertEqual(sidecar["service_generation"], 43)
        self.assertEqual(sidecar["plan_sha256"], self.original_sidecar["plan_sha256"])
        self.assertEqual(sidecar["result_sha256"], self.original_sidecar["result_sha256"])
        self.assertEqual(sidecar["build_info"]["product_version"], "1.2.3")
        self.assertEqual(marker["product_version"], "1.2.3")
        self.assertEqual(json.loads((self.directory / "release-notes-operation.json").read_bytes()), sidecar)
        with patch.object(release_notes_capture, "provider_json", side_effect=[self.service(), self.revision()]), \
                patch.object(release_notes_capture, "build_info", return_value=build):
            self.assertTrue(release_notes_capture.active_marker_matches(self.directory, "rollback", "701", 1, marker))

    def test_fresh_later_generation_does_not_attach_to_old_operation_sidecar(self):
        responses = [self.service(), self.revision()]
        build = self.original_sidecar["build_info"]
        with patch.object(release_notes_capture, "provider_json", side_effect=responses), \
                patch.object(release_notes_capture, "build_info", return_value=build):
            marker, sidecar_bytes = release_notes_capture.capture(self.directory, "rollback", "701", 1)
        with patch.object(release_notes_capture, "provider_json", side_effect=[self.service(generation=44), self.revision()]), \
                patch.object(release_notes_capture, "build_info", return_value=build):
            self.assertFalse(release_notes_capture.active_marker_matches(self.directory, "rollback", "701", 1, marker))
        self.assertEqual((self.directory / "release-notes-operation.json").read_bytes(), sidecar_bytes)


if __name__ == "__main__":
    unittest.main()
