#!/usr/bin/env python3
"""Pipeline entrypoint: patches OLW healthcheck, runs pipeline, regenerates JSONL."""
import os
import sys

# Bypass OLW healthcheck (DeepSeek /models endpoint unreliable on Cloud Run)
from obsidian_llm_wiki.openai_compat_client import OpenAICompatClient
OpenAICompatClient.require_healthy = lambda self: None
OpenAICompatClient.healthcheck = lambda self: True

# Ensure OLW config exists
os.makedirs(os.path.expanduser("~/.config/olw"), exist_ok=True)
api_key = os.environ.get("DEEPSEEK_API_KEY", "")
config_path = os.path.expanduser("~/.config/olw/config.toml")
if api_key and not os.path.exists(config_path):
    with open(config_path, "w") as f:
        f.write(f'api_key = "{api_key}"\n')

# Run OLW command
cmd = sys.argv[1] if len(sys.argv) > 1 else "run"
user = os.environ.get("USER_ID", "test-user")
project = os.environ.get("PROJECT_ID", "demo")
vault = f"/data/users/{user}/projects/{project}"

from obsidian_llm_wiki.cli import cli
sys.argv = ["olw", cmd, "--vault", vault]
if cmd == "run":
    sys.argv.append("--auto-approve")
cli()

# Regenerate JSONL cache after successful run
if cmd == "run":
    from subprocess import run
    run(["python3", "/regenerate-jsonl.py"], check=True)
