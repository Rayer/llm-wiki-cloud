# LLM Wiki BFF

Backend-for-frontend API for LLM Wiki. It serves project-scoped wiki sources, concepts, ID routing, import, search/cache/index, and pipeline status endpoints.

## Requirements

- Go 1.26
- Google Cloud ADC for the real local GCS/Firestore development target
- `gcloud` and Docker registry access for deploys

## Test

Run the full Go test suite:

```sh
go test ./...
```

Run the local filesystem store tests only:

```sh
go test ./internal/localfs
```

## Local Development

Native local development uses ADC with the project `llm-wiki-cloud`, bucket
`llm-wiki-cloud-local`, and Firestore database `llm-wiki-cloud-local`. The
bucket/database must already be provisioned. The local app uses stable
worktree-specific `local_scopes/{scope}` roots in Firestore and GCS and does
not run pipeline jobs on Cloud Run.

From the monorepo root, run `make bootstrap` once, then `make local-start`.
Bootstrap installs the pinned Synto wheel in a worktree-private Python
environment used by the BFF's native worker. This creates the default password
fixture only when missing; it does not reset an existing account. The admin
fixture is `admin-local@llm.wiki.dev`; its stable local-only password is stored
in `deploy/cac/local_fixture.pkl`. The passwordless Demo button uses a separate
configured Demo account and creates an empty default project when needed. BFF
APIs require a formal Bearer token and do not accept `X-User-ID` as identity.
Default URLs are Frontend
`http://localhost:3000`, BFF `http://localhost:8080`, and Auth
`http://localhost:8081`; use the `localhost` host for local HTTP and cookies.

`make local-stop` stops only this worktree's supervisor-managed processes.
For component debugger workflows, port overrides, ADC setup, account lifecycle,
scope retention, troubleshooting, and pipeline cost boundaries, see
[docs/LOCAL_DEV.md](docs/LOCAL_DEV.md).

## Deploy

The final environment mapping and release process are documented in [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md). The Makefile uses a full commit SHA image tag and development-only deploy defaults; production promotion is available only through the release-gated GitHub workflow.

Build image:

```sh
make docker-build
```

Push image:

```sh
make docker-push
```

Deploy to Cloud Run:

```sh
make deploy
```

Build, push, and deploy:

```sh
make all
```

Deployed mode expects GCP credentials and uses GCS/Firestore. The native local
cloud workflow is configured by `LOCAL_CLOUD_SCOPE`; filesystem storage and
synthetic `--local`/`DEV_JWT` auth modes are not available.

### Immutable worker generations

The Cloud Run worker uses the GCS API directly; it does not mount the bucket.
Cloud mode requires `BUCKET`, `USER_ID`, and `PROJECT_ID` and publishes a
create-only generation under `.lwc/publish/generations/`, then commits
`.lwc/publish/current.json` with a GCS generation precondition. The BFF reads
the manifest view when it exists and retains direct legacy reads only for
projects that have not yet published a manifest. `--vault` remains the local
developer workflow and is never used by the deployed worker.

The BFF supports `FIRESTORE_DATABASE_ID`, `PIPELINE_JOB_URL`, and `ALLOWED_ORIGINS` environment overrides. Auth additionally requires the exact `ALLOWED_HOSTS` Host allowlist. Empty database and pipeline values preserve the legacy defaults; configured pipeline URLs must be HTTPS Cloud Run Jobs `:run` URLs on `run.googleapis.com` with the expected resource path.

Query retrieval is typed configuration: `query_expansion_keywords_per_attempt` / `QUERY_EXPANSION_KEYWORDS_PER_ATTEMPT` defaults to `24` and accepts `1..100`; `query_expansion_attempts` / `QUERY_EXPANSION_ATTEMPTS` defaults to `3` and accepts `1..10`; `query_selection_evidence_threshold` / `QUERY_SELECTION_EVIDENCE_THRESHOLD` defaults to `2` and accepts `1..100`; and `query_matching_rare_keyword_max_document_frequency` / `QUERY_MATCHING_RARE_KEYWORD_MAX_DOCUMENT_FREQUENCY` defaults to `1` and accepts `1..1000`. Selection limit defaults to `10` and accepts `1..1000`; exploration slots default to `1` and accept `0..limit`. Positive expansion keywords are bounded per attempt, calls share the request context and run concurrently, and aggregation is deterministic by attempt index. Candidates remain hard-eligible first and require exact identity, consensus support, or the configured rare lexical exception; an empty result reports insufficient evidence rather than corpus or world absence.

## Useful Commands

```sh
make build-sync
go test ./internal/localfs
```

Audit existing Firestore users before enabling canonical-email reservations. The
audit is dry-run by default; `--apply` is an explicit write gate and refuses to
write when validation finds collisions or malformed records:

```sh
go run ./cmd/auth_identity_audit --dry-run
go run ./cmd/auth_identity_audit --apply
```

## Pipeline rate limits (LWC-138)

User `POST /api/v1/pipeline/run` enforces per-project quotas before dispatching
to the native local worker or deployed Cloud Run job:

| Env | Default | Meaning |
|-----|---------|---------|
| `PIPELINE_DAILY_LIMIT` | 2 | Max accepted runs per project per UTC day |
| `PIPELINE_COOLDOWN_SECONDS` | 3600 | Min seconds between accepted runs |
| `PIPELINE_MIN_NEW_RAW` | 1 | Require this many new/modified raw files since last run |
| `PIPELINE_DEMO_USER_IDS` | (empty) | Comma-separated user IDs blocked from pipeline |

Configurations without a Firestore quota backend report `quota.enforced=false`;
native local startup requires its configured Firestore database to be reachable.
Admin pipeline trigger skips daily/cooldown/new-raw but still blocks if already
running.
