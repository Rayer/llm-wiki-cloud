"""Local replay of the observed provider envelope and document hash contract."""
import hashlib
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location(
    "frontend_build_config", Path(__file__).resolve().parents[1] / "deploy/components/frontend_build_config.py"
)
parser = importlib.util.module_from_spec(spec)
spec.loader.exec_module(parser)

DOCUMENT = b'{"schema_version":1,"api_url":"https://llm-wiki-bff-dev-580854833715.asia-east1.run.app","auth_url":"https://auth.dev.rayer.idv.tw"}'


def envelope(body=DOCUMENT, boundary=b"provider-boundary", headers=b"Content-Type: application/json"):
    return b"--" + boundary + b"\r\n" + headers + b"\r\n\r\n" + body + b"\r\n\r\n--" + boundary + b"--\r\n"


class FrontendBuildConfigTests(unittest.TestCase):
    def test_observed_document_hash_ignores_envelope_and_surrounding_whitespace(self):
        for body in (DOCUMENT, DOCUMENT + b"\r\n", envelope(), envelope(boundary=b"another-random-boundary")):
            with self.subTest(body=body[:30]):
                extracted = parser.document(body)
                self.assertEqual(extracted, DOCUMENT)
                self.assertEqual(hashlib.sha256(extracted).hexdigest(),
                                 "bcb67028cae0a0eadaef9da1b8cdfbb293f29679febf3022bfffbd5e72467a98")

    def test_rejects_malformed_or_ambiguous_mime(self):
        bodies = [
            envelope()[:-6], envelope() + b"extra", b"--missing-newline",
            envelope().replace(b"\r\n", b"\n"),
            envelope(headers=b"Content-Type: text/plain"),
            envelope(headers=b"Content-Type: application/json\r\nContent-Type: application/json"),
            envelope(headers=b"Content-Type: application/json\r\nmalformed header"),
            envelope(headers=b"Content-Type: application/json\r\nContent-Transfer-Encoding: base64"),
            envelope(headers=b'Content-Type: application/json\r\nContent-Disposition: attachment; filename="other.json"'),
            envelope(headers=b"Content-Type: multipart/mixed; boundary=nested"),
            envelope().replace(b"--provider-boundary--", b"--provider-boundary\r\nContent-Type: application/json\r\n\r\n{}\r\n--provider-boundary--"),
            envelope(body=b"x" * 4097), b"x" * 8193, b"",
        ]
        for body in bodies:
            with self.subTest(body=body[:80]):
                with self.assertRaises(ValueError):
                    parser.document(body)


if __name__ == "__main__":
    unittest.main()
