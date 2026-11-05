"""Exercise the canonical BFF descriptor through the real MCP SDK discovery."""
import copy
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[1]
FOLLOW_THROUGH_START = "Use query_project first and keep normal answers query-only"
SDK_TEST = "TestMCPMetadataAdvertisesCanonicalClientFollowThrough"


class MCPClientGuidanceTests(unittest.TestCase):
    def test_canonical_descriptor_reaches_sdk_and_old_text_fails_for_missing_guidance(self):
        scratch = Path(os.environ.get("TMPDIR", tempfile.gettempdir()))
        scratch.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(prefix="lwc-mcp-client-guidance-", dir=scratch) as temporary:
            work = Path(temporary)
            descriptor_dir = work / "canonical-descriptor"
            descriptor_dir.mkdir()
            env = dict(os.environ)
            env.update({
                "LWC_REPOSITORY_ROOT": str(ROOT),
                "TMPDIR": str(scratch),
                "GOOGLE_APPLICATION_CREDENTIALS": "/dev/null",
                "GCE_METADATA_HOST": "127.0.0.1:9",
                "GCE_METADATA_IP": "127.0.0.1",
                "CLOUDSDK_CONFIG": str(work / "empty-cloud-config"),
                "GOPROXY": "off",
                "GOSUMDB": "off",
            })
            producer = subprocess.run(
                ["go", "run", "./cmd/pipeline_config", "prepare", "--target", "bff",
                 "--descriptor", "--environment", "dev", "--output", str(descriptor_dir)],
                cwd=ROOT / "apps/bff", env=env, capture_output=True, text=True, check=False,
            )
            self.assertEqual(producer.returncode, 0,
                             f"canonical BFF descriptor producer failed: {producer.stdout}\n{producer.stderr}")
            descriptor = json.loads((descriptor_dir / "bff-inputs.json").read_text(encoding="utf-8"))
            instructions = descriptor.get("mcp", {}).get("instructions")
            self.assertIsInstance(instructions, str)
            self.assertIn(FOLLOW_THROUGH_START, instructions)

            new_descriptor = work / "new-guidance.json"
            new_descriptor.write_text(json.dumps(descriptor), encoding="utf-8")
            current = subprocess.run(
                ["go", "test", "./internal/handler/v1", "-run", f"^{SDK_TEST}$", "-count=1", "-v"],
                cwd=ROOT / "apps/bff", env={**env, "LWC_MCP_GUIDANCE_DESCRIPTOR": str(new_descriptor)},
                capture_output=True, text=True, check=False,
            )
            self.assertEqual(current.returncode, 0,
                             f"canonical instructions did not reach SDK discovery: {current.stdout}\n{current.stderr}")

            before, marker, after = instructions.partition(FOLLOW_THROUGH_START)
            self.assertTrue(marker, "new guidance paragraph was not found")
            old_text = before.rstrip() + "\n\n" + after.split("\n\n", 1)[1].lstrip()
            old_descriptor = copy.deepcopy(descriptor)
            old_descriptor["mcp"]["instructions"] = old_text
            old_path = work / "old-guidance.json"
            old_path.write_text(json.dumps(old_descriptor), encoding="utf-8")
            negative = subprocess.run(
                ["go", "test", "./internal/handler/v1", "-run", f"^{SDK_TEST}$", "-count=1", "-v"],
                cwd=ROOT / "apps/bff", env={**env, "LWC_MCP_GUIDANCE_DESCRIPTOR": str(old_path)},
                capture_output=True, text=True, check=False,
            )
            output = negative.stdout + negative.stderr
            self.assertNotEqual(negative.returncode, 0, "old instructions unexpectedly passed the guidance assertion")
            self.assertIn("MCP SDK discovery completed with configured descriptor", output)
            self.assertIn("missing follow-through guidance after MCP SDK discovery", output)


if __name__ == "__main__":
    unittest.main()
