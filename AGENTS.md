# AGENTS.md

Guidance for AI coding agents working in this repository.

## Project Overview

This repo contains the Cloud Run worker for `llm-wiki-cloud`, an OLW pipeline running on GCP. The worker coordinates:

- GCS input/output under `gs://llm-wiki-data/users/{uid}/projects/{pid}/`
- Firestore locks in the default database
- Gemini API credentials from Secret Manager
- OLW ingest, compile, and publish operations

The static frontend mockup is kept in `frontend-mockup.html` and is intentionally dependency-free.

## Repository Layout

- `worker.py` - main Cloud Run Job entrypoint: lock, sync, OLW pipeline, sync, unlock.
- `watchdog.py` - stale Firestore lock cleanup job.
- `Dockerfile` - Python 3.12 image with `obsidian-llm-wiki` and Google Cloud CLI.
- `Makefile` - build, deploy, run, logs, status, and watchdog commands.
- `README.md` - operational setup, architecture, and current status.
- `frontend-mockup.html` - static HTML mockup with realistic data.

## Development Rules

- Keep changes small and operationally conservative; this code runs against real GCP resources.
- Prefer standard-library Python unless a dependency is already provided by the container image.
- Do not hard-code secrets. Runtime secrets should come from Secret Manager or environment variables.
- Preserve the Firestore lock lifecycle: acquire before pipeline work, release in cleanup paths.
- Be careful with GCS paths. Tenant/project paths are part of the data isolation model.
- Avoid broad refactors unless they directly support the requested change.

## Verification

Use the narrowest check that proves the change:

```bash
python3 -m py_compile worker.py watchdog.py
```

For operational checks, use the Makefile targets documented in `README.md`:

```bash
make status
make build
make deploy
make run
make logs-id ID=<execution-id>
```

Only run deploy/build/run targets when the user explicitly wants to interact with GCP, because they can change cloud state or consume quota.

## GCP Context

Default project settings in the Makefile:

- Project: `llm-wiki-cloud`
- Region: `asia-east1`
- Worker job: `olw-worker`
- Watchdog job: `lock-watchdog`
- Bucket: `llm-wiki-data`
- Default test user/project: `test-user` / `demo`

## Frontend Mockup

`frontend-mockup.html` is a standalone static file. Keep it zero-dependency unless the user asks for a real frontend app or build system.

When editing the mockup:

- Keep the first screen usable, not a marketing landing page.
- Preserve realistic wiki/query flows and source-link behavior.
- Check responsive layout manually if changing structure or CSS.

## Deployment Safety

- `make build` submits a Cloud Build and pushes an image.
- `make deploy` updates the existing Cloud Run Job.
- `make deploy-fresh` deletes and recreates the worker job.
- `make watchdog-deploy` deletes and recreates the watchdog job.
- `make clean` deletes both Cloud Run jobs.

Treat these commands as cloud-state-changing operations. Ask before running them unless the user explicitly requested deployment or cleanup.

## Style Notes

- Python target is 3.12.
- Keep logging simple and suitable for Cloud Run logs.
- Use explicit environment variable names matching the Makefile and Cloud Run job configuration.
- Prefer clear helper functions over adding new framework structure.
