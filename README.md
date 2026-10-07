# LLM Wiki Cloud

LLM Wiki Cloud is a project-scoped wiki application. It combines a Next.js
frontend, a Go backend-for-frontend (BFF), a standalone Auth service, and a
pipeline worker that turns wiki inputs into searchable sources, concepts, and
suggested queries.

## What it does

- Sign in and work in one or more projects.
- Search project content in `wiki` mode, or request a synthesized answer in
  `full` mode.
- Browse sources and concepts, including source detail and citations.
- Import raw files, inspect pipeline status, and review generated content.
- Run the project pipeline and rebuild the local index when developing.
- Use admin-only project and system controls where the authenticated role
  permits them.

## Architecture and data flow

```text
Browser (Next.js frontend)
        │ login/session                 │ project-scoped API calls
        ▼                               ▼
  Auth service ───────────────▶ Go BFF API
                                      │
                    ┌─────────────────┴─────────────────┐
                    ▼                                   ▼
            Native local worker                  Cloud Run worker
                    └───────────────┬───────────────────┘
                                    ▼
                              GCS + Firestore
```

Native local development uses ADC, real GCS/Firestore resources, and a
worktree-scoped data root. The local BFF starts a native worker process;
deployed mode keeps its Cloud Run worker. See the [local development
guide](apps/bff/docs/LOCAL_DEV.md) and [deployment authority and
runbook](apps/bff/docs/DEPLOYMENT.md) for the operational details.

## Repository layout

| Path | Responsibility |
| --- | --- |
| [`apps/bff/`](apps/bff/) | Go BFF, Auth service, pipeline worker, tests, and API docs |
| [`apps/frontend/`](apps/frontend/) | Next.js web application and frontend tests |
| [`scripts/`](scripts/) | Repository-level smoke and deployment-contract checks |
| [`.github/workflows/`](.github/workflows/) | Canonical CI/CD workflows |
| [`docs/`](docs/) | Repository-level operational history and gates |

## Prerequisites

The versions below match canonical CI:

- Go 1.26
- Node.js 22 and npm

Google Cloud ADC and access to the pre-provisioned local GCS bucket and
Firestore database are required for the native local app. Container registry
access is only needed for deployment work.

## Staged experiments

For raw-input-to-query experiments, use the [single-container directory workflow](docs/local-staged-e2e.md).
Build the worker Dockerfile's `experiment` target, prepare input/config in one
directory, and bind-mount it with a new child output such as `run/`. The image
contains the Go staged runner, worker, and pinned public Synto CLI; no application
services are needed. The guide includes Apple container commands, external image
digest metadata, host-only read-only DEV snapshot preparation, and parent checks.

## Run locally

From the repository root, bootstrap dependencies once, then start the local
services:

```sh
make bootstrap && make local-start
```

Open the local-only app at <http://localhost:3000>. The local services are:

- Frontend: <http://localhost:3000>
- BFF: <http://localhost:8080>
- Auth: <http://localhost:8081>
- BFF Swagger UI: <http://localhost:8080/swagger/index.html>

Use the normal password login with the local admin/test fixture:

```text
email: admin-local@llm.wiki.dev
password: read from deploy/cac/local_fixture.pkl
```

The fixture's password is stable and local-only. The account is created only
if its email is missing and is not reset on startup. The passwordless Demo
button uses a separate restricted account. Local data uses a stable scope
unique to this worktree in the `llm-wiki-cloud-local` bucket/database. The
browser, BFF, Auth, and native worker share that scope. The local pipeline runs
as a native worker process and
does not call Cloud Run. Use `make local-stop` to stop only this worktree's
supervisor-managed processes.

For ADC setup, component debugger workflows, port overrides, auth cookies,
data retention, and troubleshooting, read
[Local Development](apps/bff/docs/LOCAL_DEV.md).

## Everyday commands

Run these from the repository root:

```sh
make bootstrap       # install app dependencies, pinned Synto venv, and local frontend config
make local-start     # start BFF, Auth, and frontend
make local-stop      # stop this worktree's supervised local processes
make lint            # frontend lint
make typecheck       # frontend TypeScript check
make vet             # Go vet
make test            # BFF contract/race tests and frontend tests
make build           # BFF and production frontend builds
make smoke           # loopback/auth, scope, and optional emulator worker tests
make verify          # bootstrap plus the complete local verification gate
```

`make workflow-yaml` validates the canonical CI workflow and its permissions;
it is included by `make verify`.

## Verification and CI contract

The canonical workflow is [`ci.yml`](.github/workflows/ci.yml). It runs on
pushes to and pull requests targeting `main` or `develop`, and can also be
dispatched manually. The gate covers:

- Go version validation, deployment-contract tests, legacy CD safety tests,
  `go vet`, race-enabled Go tests, and Go builds;
- frontend `npm ci`, lint, typecheck, tests, and production build;
- the local vertical smoke test;
- workflow-source guards and actionlint/schema validation; and
- an aggregate `canonical-ci` job that requires every canonical job to pass.

Before opening a change, the shortest full local contract is:

```sh
make verify
```

## Development and API references

- [BFF README](apps/bff/README.md) — backend responsibilities and focused commands.
- [Frontend README](apps/frontend/README.md) — frontend-specific development notes.
- [Local Development](apps/bff/docs/LOCAL_DEV.md) — component isolation, ports, auth, and local data.
- [Deployment](apps/bff/docs/DEPLOYMENT.md) — environment mapping, CD authority, IAM, rollback, and break-glass procedures.
- [Query experiment](apps/bff/cmd/query_experiment/README.md) — controlled runs against frozen project snapshots.
- [OpenAPI YAML](apps/bff/docs/swagger.yaml) — generated BFF API contract.
- [OpenAPI JSON](apps/bff/docs/swagger.json) — generated BFF API contract in JSON.

## Deployment boundary

Development deployment is workflow- and configuration-controlled. Production
promotion is workflow-gated: it consumes validated development evidence and a
full commit SHA, then promotes an immutable image digest. The deployment
workflow and [Deployment](apps/bff/docs/DEPLOYMENT.md) document are the
authority for release, rollback, IAM, and break-glass operations. This README
intentionally provides no manual production mutation commands.

## Historical note

This repository began as a Phase 1 migration from separate BFF and frontend
repositories. The monorepo is now canonical; the old migration baseline is
retained only as history in Git, not as the current operating model.
