# LLM Wiki Cloud

Phase 1 migration baseline:

- `apps/bff/` imports `Rayer/llm-wiki-bff` at `e03c8cedbc14e4041ca69ae90772548cdb9a51ea`.
- `apps/frontend/` imports `Rayer/llm-wiki-frontend` at `71470ee907c02f78ac46e0ba3ab141ebd4252384`.

The retired cloud repository is preserved at [Rayer/llm-wiki-cloud-legacy](https://github.com/Rayer/llm-wiki-cloud-legacy).

## Local and CI parity

The monorepo has one root contract for both imported apps:

```sh
make bootstrap       # Go modules, npm dependencies, local demo data/config
make lint            # Frontend lint
make typecheck       # Frontend TypeScript check
make vet             # Go vet
make test            # BFF validators/race tests and frontend tests
make build           # BFF and production frontend builds
make local-start     # BFF + Auth + Frontend on ports 8080, 8081, and 3000
make local-stop
make smoke           # bounded local Auth → BFF → Frontend vertical smoke
make verify          # bootstrap plus the complete local verification gate
```

Canonical CI is `.github/workflows/ci.yml`. It runs both app suites, the local
vertical smoke, workflow/source guards, and an aggregate `canonical-ci` gate on
every push and pull request to `main` or `develop`.

Deployment, release, rollback, and provider settings are intentionally not
migrated or activated in this phase. The new Go Wiki app is intentionally
deferred to LWC-305.
