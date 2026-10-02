"""TEST ONLY: run the real Auth shell adapter through real Providers.prepare()."""
import json
import os
from pathlib import Path
import sys
import tempfile
import textwrap

sys.path.insert(0, str(Path.cwd() / "deploy/engine"))
import providers
from providers import Providers
from support import Breakpoint, write

DIGEST = "sha256:" + "a" * 64
OTHER = "sha256:" + "b" * 64
PRODUCTION_RUN = providers.run


def executable(path, source):
    path.write_text(source)
    path.chmod(0o755)


def run_case(mode):
    with tempfile.TemporaryDirectory() as temp:
        root = Path(temp)
        directory = root / "release"
        directory.mkdir()
        bindir = root / "bin"
        bindir.mkdir()
        plan = {
            "source": "f" * 40,
            "branch": "develop",
            "selected": ["auth"],
            "normalized": {
                "environment": "development",
                "gcp": {
                    "artifact_registry": "asia-east1-docker.pkg.dev/llm-wiki-cloud/cloud-run-images",
                    "project_id": "llm-wiki-cloud",
                },
            },
        }
        write(directory / "plan.json", plan)
        event_file = root / "events.json"
        gcloud = textwrap.dedent(
            """            #!/usr/bin/env python3
            import json, os, sys, time
            from pathlib import Path
            args = sys.argv[1:]
            mode = os.environ['DIAG_MODE']
            DIGEST = 'sha256:' + 'a' * 64
            OTHER = 'sha256:' + 'b' * 64
            event_file = Path(os.environ['DIAG_EVENTS'])
            events = json.loads(event_file.read_text()) if event_file.exists() else []
            if args[:2] == ['builds', 'submit']:
                events.append('submit')
                event_file.write_text(json.dumps(events))
                if mode == 'submit-times-out-after-accept':
                    time.sleep(3)
                if mode == 'submit-fails-after-accept':
                    sys.stderr.write('TEST ONLY simulated failure after acceptance\\n')
                    sys.exit(9)
                sys.exit(0)
            if args[:4] == ['artifacts', 'docker', 'images', 'describe']:
                is_tag_lookup = '@sha256:' not in args[4]
                events.append('tag-digest' if is_tag_lookup else 'registry-validation')
                event_file.write_text(json.dumps(events))
                if is_tag_lookup and mode == 'digest-lookup-fails':
                    sys.stderr.write('TEST ONLY simulated lookup failure\\n')
                    sys.exit(8)
                if is_tag_lookup and mode == 'malformed-digest':
                    print('sha256:malformed')
                    sys.exit(0)
                if not is_tag_lookup and mode == 'registry-read-fails':
                    sys.stderr.write('TEST ONLY simulated validation failure\\n')
                    sys.exit(7)
                digest = OTHER if not is_tag_lookup and mode == 'registry-mismatch' else DIGEST
                print(digest)
                sys.exit(0)
            sys.stderr.write('TEST ONLY unexpected operation\\n')
            sys.exit(3)
            """
        )
        executable(bindir / "gcloud", gcloud)
        executable(bindir / "timeout", '#!/bin/sh\nshift 3\nexec "$@"\n')
        executable(bindir / "go", '#!/bin/sh\nprintf "1.0.0\\n"\n')
        env = {
            key: value for key, value in os.environ.items()
            if key in ("HOME", "TMPDIR", "LANG", "LC_ALL", "SYSTEMROOT", "PATH")
        }
        env.update({
            "PATH": str(bindir) + os.pathsep + env["PATH"],
            "PLAN_PATH": str(directory / "plan.json"),
            "ROOT": str(Path.cwd()),
            "SOURCE_SHA": plan["source"],
            "SOURCE_REF": "develop",
            "GITHUB_RUN_ID": "1",
            "GITHUB_RUN_ATTEMPT": "1",
            "DIAG_MODE": mode,
            "DIAG_EVENTS": str(event_file),
        })
        provider = Providers(plan, directory)
        old_env = os.environ.copy()
        providers.run = PRODUCTION_RUN
        if mode == "submit-times-out-after-accept":
            def short_test_timeout(args, **kwargs):
                if args and str(args[0]) == "bash":
                    kwargs["timeout"] = 1
                return PRODUCTION_RUN(args, **kwargs)
            providers.run = short_test_timeout
        try:
            os.environ.clear()
            os.environ.update(env)
            try:
                provider.prepare("auth")
                outcome = "receipt-returned"
            except Breakpoint as exc:
                outcome = f"{exc.reason}/{exc.status}/{exc.action}"
            events = json.loads(event_file.read_text()) if event_file.exists() else []
            print(mode, "=>", outcome, "events=", ",".join(events) or "none")
        finally:
            providers.run = PRODUCTION_RUN
            os.environ.clear()
            os.environ.update(old_env)


for case in (
    "success",
    "submit-fails-after-accept",
    "submit-times-out-after-accept",
    "digest-lookup-fails",
    "malformed-digest",
    "registry-mismatch",
    "registry-read-fails",
):
    run_case(case)
