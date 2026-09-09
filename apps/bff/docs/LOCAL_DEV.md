# Local Development

LLM Wiki Cloud now uses one monorepo. Run the commands below from its root
unless a component directory is specified. The root `Makefile` delegates to
`apps/bff/Makefile`; the frontend is `apps/frontend`, not a sibling repository.

```text
llm-wiki-cloud/
  Makefile
  apps/bff/              # BFF, Auth, pipeline, local fixtures
  apps/frontend/         # Next.js frontend
```

## First-time setup

Prerequisites: Go 1.26 and Node.js 22 with npm (the canonical CI versions).

```bash
make bootstrap
```

This installs dependencies, writes `apps/frontend/.env.local`, and resets
`apps/bff/local-data/` from the checked-in demo fixtures. **Do not run bootstrap
or seed if you need to preserve modified local data.** For an existing checkout:

```bash
cd apps/bff && go mod download
cd ../frontend && npm ci --include=dev
```

Return to the repository root before running the next commands.

## Start the normal app

```bash
make local-start
```

This starts BFF, Auth, and frontend and creates local data only if it is absent.
Use **http://localhost:3000** in the browser. The generated API/Auth URLs also
use `localhost`; keep the same hostname when testing refresh cookies, rather
than mixing `127.0.0.1` and `localhost`.

| Service | Default URL |
| --- | --- |
| Frontend | http://localhost:3000 |
| BFF | http://localhost:8080 |
| Auth | http://localhost:8081 |
| Swagger | http://localhost:8080/swagger/index.html |

Click **試用 Demo** for the read-only UI experience. For normal local account
flows, sign in with the local-only fixture credentials:

```text
email: demo@llm-wiki.dev
password: demo123456
user ID: local-user
project ID: demo
role: admin
```

The local fixture corpus is smaller than the deployed demo. Normal UI browsing
and local keyword retrieval require no provider key; generated answers and a
full provider-backed pipeline require a configured LLM provider. Do not treat a
local UI check as evidence that provider-backed generation was tested.

## Isolated ports / multiple worktrees

Override all three ports to avoid colliding with another checkout. Auth and BFF
CORS must also allow the actual frontend origin: `FRONTEND_PORT` alone does not
update the allowlist.

```bash
ALLOWED_ORIGINS=http://localhost:13000,http://127.0.0.1:13000 \
  make local-start BFF_PORT=18080 AUTH_PORT=18081 FRONTEND_PORT=13000
```

Open **http://localhost:13000**. BFF is on `18080`, Auth on `18081`.
This is the isolated setup used for the LWC-326 UI demo.

`make local-start` writes the matching public URLs into
`apps/frontend/.env.local`. Restart the frontend after changing these values:

```dotenv
NEXT_PUBLIC_API_URL=http://localhost:18080
NEXT_PUBLIC_AUTH_URL=http://localhost:18081
NEXT_PUBLIC_DEV_USER_ID=local-user
NEXT_PUBLIC_DEV_PROJECT_ID=demo
```

`403` on an Auth `OPTIONS` request usually means the browser's origin is missing
from `ALLOWED_ORIGINS`. Set it on the support processes as well when splitting
services across terminals.

## Isolate a component

Run these from the monorepo root, in separate terminals as indicated.

| Work on | Support terminal | Component terminal |
| --- | --- | --- |
| BFF | `make -C apps/bff support-bff` (Auth + frontend) | `make -C apps/bff bff-local` |
| Frontend | `make -C apps/bff support-frontend` (BFF + Auth) | `make -C apps/bff frontend-local` |
| Pipeline | `make -C apps/bff support-pipeline` (BFF + Auth + frontend) | `make -C apps/bff pipeline-test` |

Pass the same port overrides to both terminals. For custom frontend ports,
export the `ALLOWED_ORIGINS` shown above in each terminal that launches BFF/Auth.
The support/full targets generate local config and ensure fixtures exist;
standalone `bff-local`/`auth-local` targets do not seed data.

Only run a full provider-backed pipeline when needed:

```bash
LLM_API_KEY=... make -C apps/bff pipeline-run
```

Provide the real key privately in your terminal environment, never in source
control or a shared transcript.

## BFF payload debugging without Auth

For Swagger, curl, or IDE breakpoints, prepare fixtures if needed and start BFF:

```bash
make -C apps/bff ensure-local-data
make -C apps/bff bff-local
```

The Makefile enables local-only `DEV_JWT=true`. Use `X-User-ID` without a token;
project-scoped requests also need `X-Project-ID`:

```bash
curl -fsS -H 'X-User-ID: local-user' \
  http://localhost:8080/api/v1/projects
curl -fsS -H 'X-User-ID: local-user' -H 'X-Project-ID: demo' \
  http://localhost:8080/api/v1/concepts
curl -fsS -H 'X-User-ID: local-user' \
  http://localhost:8080/api/v1/admin/settings
```

Local requests without an explicit role default to admin; use
`X-User-Role: user` to exercise non-admin denial. Missing user identity returns
401. If an Authorization header is present, Bearer validation takes precedence:
an invalid token does not fall back to the local header. Storage/project/provider
requirements still apply after authorization.

In Swagger, authorize `DevUserAuth: local-user` and, for project-scoped requests,
`ProjectHeader: demo`. If an operation's generated security declaration does not
send these headers, use curl. Swagger parity/history is tracked in LWC-265.

## Auth-flow testing

Full and support targets include Auth. Start it independently only when needed:

```bash
make -C apps/bff auth-local
```

Get a fresh 15-minute token without displaying it:

```bash
TOKEN="$(make -s -C apps/bff local-token)"
curl -fsS -H "Authorization: Bearer $TOKEN" \
  http://localhost:8080/api/v1/admin/settings
```

For overridden ports, add `AUTH_PORT=18081` to `local-token` and call BFF on
`18080`. The demo credentials and `JWT_SECRET=dev-secret` are local-only.

Deployed DEV uses `DEV_JWT=false` and https://auth.dev.rayer.idv.tw. Local
`X-User-ID` access is deliberately unavailable there. Follow the
[LWC DEV QA Runbook](https://irisnode.youtrack.cloud/articles/LWC-A-12) for deployed
identities, targets, and mutation boundaries.

## Verify

```bash
make lint
make typecheck
npm --prefix apps/frontend test
npm --prefix apps/frontend run build
```

The complete repository gate is `make verify`, which also bootstraps/resets
fixtures and runs backend and smoke checks. `make smoke` starts its own services
(default `13000/18080/18081`); stop your demo first or give smoke separate ports.
Do not run fixture-resetting gates while preserving demo uploads.

## Stop and reset

Press Ctrl-C in the service terminal. For orphaned processes, stop listeners on
the exact ports owned by this checkout:

```bash
make local-stop
# Or, for the isolated example:
make local-stop BFF_PORT=18080 AUTH_PORT=18081 FRONTEND_PORT=13000
```

This command stops **all** listeners on those ports, so verify they belong to
your local run before using it. Reset fixture data only when intended:

```bash
make -C apps/bff seed
```

There is no root `make seed` target. `seed` deletes and recreates
`apps/bff/local-data/`.
