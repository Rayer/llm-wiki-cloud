#!/usr/bin/env python3
"""
Firestore Lock Watchdog — cleans up stale locks.

Run as a Cloud Run Job on a schedule (e.g. every 5-10 minutes).
Reads all active locks from Firestore, releases any that have expired.
"""

import json
import os
import sys
from datetime import datetime, timezone
from urllib import request, error as urllib_error

PROJECT = "llm-wiki-cloud"
FIRESTORE_BASE = (
    f"https://firestore.googleapis.com/v1/projects/{PROJECT}"
    "/databases/(default)/documents"
)
LOCKS_URL = f"{FIRESTORE_BASE}/locks"


def get_access_token() -> str:
    import subprocess
    result = subprocess.run(
        ["gcloud", "auth", "print-access-token"],
        capture_output=True, text=True, check=True,
    )
    return result.stdout.strip()


def firestore_request(method: str, url: str, token: str, body: dict | None = None) -> dict:
    headers = {
        "Authorization": f"Bearer {token}",
        "Content-Type": "application/json",
    }
    data = json.dumps(body).encode() if body else None
    req = request.Request(url, data=data, headers=headers, method=method)
    try:
        with request.urlopen(req, timeout=10) as resp:
            return json.loads(resp.read())
    except urllib_error.HTTPError as e:
        if e.code == 404:
            return {}
        raise


def main():
    now = datetime.now(timezone.utc)
    now_iso = now.strftime("%Y-%m-%dT%H:%M:%SZ")
    print(f"=== Lock Watchdog ({now_iso}) ===")

    token = get_access_token()

    # List all lock documents
    print("Scanning for stale locks...")
    all_locks = firestore_request("GET", LOCKS_URL, token)
    documents = all_locks.get("documents", [])

    released = 0
    for doc in documents:
        name_parts = doc.get("name", "").split("/")
        lock_id = name_parts[-1] if name_parts else "unknown"
        fields = doc.get("fields", {})
        status = fields.get("status", {}).get("stringValue", "")
        expires_str = fields.get("expires_at", {}).get("timestampValue", "")

        if status != "active" or not expires_str:
            continue

        try:
            expires = datetime.fromisoformat(expires_str.replace("Z", "+00:00"))
        except ValueError:
            continue

        if expires >= now:
            continue  # Still valid

        # Stale lock — release it
        print(f"STALE: {lock_id} (expired {expires_str})")
        release_body = {"fields": {"status": {"stringValue": "released"}}}
        doc_url = f"{FIRESTORE_BASE}/locks/{lock_id}?updateMask.fieldPaths=status"
        firestore_request("PATCH", doc_url, token, release_body)
        released += 1

    if released == 0:
        print("No stale locks found.")
    else:
        print(f"Released {released} stale lock(s).")

    print("=== Done ===")


if __name__ == "__main__":
    main()
