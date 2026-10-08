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

CONFIG_URL = "https://config.example.test/frontend-config.json"
DOCUMENT = b'{"schema_version":1,"config_url":"https://config.example.test/frontend-config.json"}'


def envelope(body=DOCUMENT, boundary=b"provider-boundary", headers=b"Content-Type: application/json"):
    return b"--" + boundary + b"\r\n" + headers + b"\r\n\r\n" + body + b"\r\n\r\n--" + boundary + b"--\r\n"


class FrontendBuildConfigTests(unittest.TestCase):
    def test_vercel_cache_headers_preserve_document_and_hash(self):
        body = envelope(headers=b"vary: RSC, Next-Router-State-Tree, Next-Router-Prefetch\r\n"
                        b"content-type: application/json\r\n"
                        b"x-next-cache-tags: _N_T_/layout,_N_T_/build-config.json/route,_N_T_/build-config.json")
        extracted = parser.document(body)
        self.assertEqual(extracted, DOCUMENT)
        self.assertEqual(parser.validate_receipt(extracted, CONFIG_URL), {
            "schema_version": 1, "config_url": CONFIG_URL,
        })
        self.assertEqual(hashlib.sha256(extracted).hexdigest(),
                         hashlib.sha256(DOCUMENT).hexdigest())

    def test_observed_document_hash_ignores_envelope_and_surrounding_whitespace(self):
        for body in (DOCUMENT, DOCUMENT + b"\r\n", envelope(), envelope(boundary=b"another-random-boundary")):
            with self.subTest(body=body[:30]):
                extracted = parser.document(body)
                self.assertEqual(extracted, DOCUMENT)
                self.assertEqual(hashlib.sha256(extracted).hexdigest(), hashlib.sha256(DOCUMENT).hexdigest())

    def test_new_receipt_is_only_reader_schema_and_bootstrap_url(self):
        for body, expected in (
            (DOCUMENT.replace(b"config_url", b"api_url"), CONFIG_URL),
            (b'{"schema_version":2,"config_url":"' + CONFIG_URL.encode() + b'"}', CONFIG_URL),
            (b'{"schema_version":1,"config_url":"http://localhost:3000/frontend-config.json"}',
             "http://localhost:3000/frontend-config.json"),
            (b'{"schema_version":1,"config_url":"https://config.example.test/frontend-config.json","api_url":"https://api.example.test"}',
             CONFIG_URL),
            (b'{"schema_version":1,"config_url":"https://other.example.test/frontend-config.json"}', CONFIG_URL),
            (b'{"schema_version":1,"config_url":"https://config.example.test/frontend-config.json","config_url":"https://other.example.test"}', CONFIG_URL),
        ):
            with self.subTest(body=body):
                with self.assertRaises(ValueError):
                    parser.validate_receipt(body, expected)

    def test_rejects_malformed_or_ambiguous_mime(self):
        bodies = [
            envelope()[:-6], envelope() + b"extra", b"--missing-newline",
            envelope().replace(b"\r\n", b"\n"),
            envelope(headers=b"Content-Type: text/plain"),
            envelope(headers=b"Content-Type: application/json\r\nContent-Type: application/json"),
            envelope(headers=b"Content-Type: application/json\r\nvary: RSC\r\nVary: RSC"),
            envelope(headers=b"Content-Type: application/json\r\nx-next-cache-tags: tag\r\nX-Next-Cache-Tags: tag"),
            envelope(headers=b"Content-Type: application/json\r\nx-unapproved-header: value"),
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
