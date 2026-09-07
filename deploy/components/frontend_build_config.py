"""Extract bounded Vercel output; hash input is JSON bytes sans JSON whitespace.

The provider sends multipart framing inside application/octet-stream. Boundaries
and the extra part CRLF are transport, never part of the config fingerprint.
Plain public JSON uses exactly the same extraction/hashing contract.
"""
import re
import sys
from email import policy
from email.parser import BytesParser


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
                or set(headers) - {"content-type", "content-disposition", "content-transfer-encoding"}
                or headers.count("content-type") != 1
                or part.get_content_type() != "application/json"
                or part.get_content_charset() not in (None, "utf-8")
                or part.get("Content-Transfer-Encoding", "binary").lower() not in ("binary", "8bit")
                or part.get_filename() not in (None, "build-config.json")
                or any(value.defects for value in part.values())):
            raise ValueError("JSON part")
        body = part.get_payload(decode=True)
    # Preserve document bytes (including key order), strip only JSON whitespace.
    body = body.strip(b" \t\r\n")
    if not 0 < len(body) <= 4096:
        raise ValueError("document size")
    return body


if __name__ == "__main__":
    try:
        sys.stdout.buffer.write(document(sys.stdin.buffer.read(8193)))
    except (ValueError, UnicodeError):
        sys.exit(1)
