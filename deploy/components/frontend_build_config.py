"""Extract and validate the bounded Frontend build receipt from Vercel output.

The provider sends multipart framing inside application/octet-stream. Boundaries
and the extra part CRLF are transport and are excluded from the receipt bytes.
The receipt carries only the runtime-config reader schema and bootstrap file URL.
"""
import argparse
import json
import re
import sys
from email import policy
from email.parser import BytesParser
from urllib.parse import urlsplit


def document(body):
    if not 0 < len(body) <= 8192:
        raise ValueError("output size")
    if body.startswith(b"--"):
        boundary, separator, _ = body.partition(b"\r\n")
        if not separator or not re.fullmatch(rb"--[A-Za-z0-9'()+_,./:=?-]{1,70}", boundary):
            raise ValueError("boundary")
        # No preamble, epilogue, truncated closing delimiter, or extra parts.
        closing = b"\r\n" + boundary + b"--"
        if not (body.endswith(closing) or body.endswith(closing + b"\r\n")):
            raise ValueError("framing")
        message = BytesParser(policy=policy.default).parsebytes(
            b'Content-Type: multipart/mixed; boundary="' + boundary[2:] + b'"\r\n\r\n' + body
        )
        parts = list(message.iter_parts())
        if message.defects or message.preamble or message.epilogue or len(parts) != 1:
            raise ValueError("multipart")
        part = parts[0]
        headers = [key.lower() for key in part.keys()]
        if (part.defects or part.is_multipart() or len(headers) != len(set(headers))
                or set(headers) - {"content-type", "content-disposition", "content-transfer-encoding", "vary", "x-next-cache-tags"}
                or headers.count("content-type") != 1
                or part.get_content_type() != "application/json"
                or part.get_content_charset() not in (None, "utf-8")
                or part.get("Content-Transfer-Encoding", "binary").lower() not in ("binary", "8bit")
                or part.get_filename() not in (None, "build-config.json")
                or any(value.defects for value in part.values())):
            raise ValueError("JSON part")
        body = part.get_payload(decode=True)
    # Preserve document bytes (including key order), strip only transport whitespace.
    body = body.strip(b" \t\r\n")
    if not 0 < len(body) <= 4096:
        raise ValueError("document size")
    return body


def _object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON key")
        result[key] = value
    return result


def validate_receipt(body, expected_config_url):
    receipt = json.loads(body.decode("utf-8"), object_pairs_hook=_object)
    if not isinstance(receipt, dict) or set(receipt) != {"schema_version", "config_url"}:
        raise ValueError("receipt fields")
    if receipt["schema_version"] != 1:
        raise ValueError("reader schema_version")
    config_url = receipt["config_url"]
    if not isinstance(config_url, str) or not config_url or config_url != config_url.strip():
        raise ValueError("config_url")
    parsed = urlsplit(config_url)
    if (parsed.scheme != "https" or not parsed.hostname or parsed.username or parsed.password
            or parsed.query or parsed.fragment):
        raise ValueError("config_url")
    if config_url != expected_config_url:
        raise ValueError("bootstrap config URL does not match normalized target")
    return receipt


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--expected-config-url")
    args = parser.parse_args()
    try:
        body = document(sys.stdin.buffer.read(8193))
        if args.expected_config_url is not None:
            validate_receipt(body, args.expected_config_url)
        sys.stdout.buffer.write(body)
    except (ValueError, UnicodeError, json.JSONDecodeError):
        sys.exit(1)
