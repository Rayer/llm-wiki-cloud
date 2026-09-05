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
                    ┌─────────────────┼─────────────────┐
                    ▼                 ▼                 ▼
              local filesystem   GCS + Firestore   Cloud Run pipeline
                                      ▲                 │
                                      └── generated wiki, indexes,
                                          and suggested queries
```

Local mode uses filesystem-backed data and disables GCP clients. Deployed mode
uses environment-specific GCS and Firestore resources; the BFF also invokes
the environment's Cloud Run pipeline job. See the [local development
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

Docker is optional for the BFF Compose integration flow. `gcloud` and access
to the configured container registry are only needed for development or
workflow-operated deployment work; they are not required for local mode.

## Run locally

From the repository root, bootstrap dependencies and seeded demo data, then
start all local services:

```sh
make bootstrap && make local-start
```

Open the local-only app at <http://localhost:3000>. The local services are:

- Frontend: <http://localhost:3000>
- BFF: <http://localhost:8080>
- Auth: <http://localhost:8081>
- BFF Swagger UI: <http://localhost:8080/swagger/index.html>

Use the documented local-only demo sign-in:

```text
email: demo@llm-wiki.dev
password: demo123456
```

These credentials and local JWT settings are for local mode only. Do not use
them in a deployed environment. Press `Ctrl-C` to stop the foreground
processes, or run `make local-stop` from the root.

For component-isolated workflows, port overrides, local tokens, and seeded
data details, read [Local Development](apps/bff/docs/LOCAL_DEV.md).

## Everyday commands

Run these from the repository root:

```sh
make bootstrap       # install dependencies and reset seeded local data
make local-start     # start BFF, Auth, and frontend
make local-stop      # stop local listeners
make lint            # frontend lint
make typecheck       # frontend TypeScript check
make vet             # Go vet
make test            # BFF contract/race tests and frontend tests
make build           # BFF and production frontend builds
make smoke           # local Auth → BFF → frontend vertical smoke
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
