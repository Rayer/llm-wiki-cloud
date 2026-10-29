# LWC-438 implementation evidence

## Change and review

Added `TestLWC438ProductionRouterUsesLoadedEnvironmentForProjectMCP` in `cmd/bff`. It serializes synthetic BFF config files, loads them through `config.LoadBFFFile`, configures the Auth consumer, and sends an authenticated project-scoped `tools/list` request through `newProductionRouter`. The local Auth double returns a valid MCP identity and checks the exact resource and project; cases also reject a wrong environment, project, resource, conflicting project header, invalid credential, and expired credential. DEV and Prod use schema 2; local uses schema 3.

The earlier handler-only tests called `MCPProjectOAuthHandler("local", ...)` directly and supplied the handler environment themselves. They bypassed both BFF file projection and production route construction, so they could not catch `BFFFile.Environment` being dropped. The regression does not assign `Config.AuthEnvironment` directly.

The existing `17b244eb17f956ac71e71c412f4fc96a2cdc86ed` source correction is sufficient: `configFromBFFFile` copies `file.Environment` to `AuthEnvironment`, which is the value `newProductionRouter` passes to `MCPProjectOAuthHandler`. The sibling Auth file projection also maps its environment in `configFromAuthFile`; no sibling source correction was needed. This change adds coverage without duplicating the production fix.

## Causal RED/GREEN evidence

- Base: `origin/develop` and worktree began at `55b771ec0537fd019ec692939dd2db3c3312cdf4`.
- Historical RED source: `4d290cb7d2f5d9d8be9523f0ddc02b8ce1b621c9`, exported from that commit with only the new test overlaid. Command: `cd /private/tmp/lwc438-red.sFx0Zr/apps/bff && GOCACHE=/private/tmp/lwc438-gocache go test ./cmd/bff -run '^TestLWC438ProductionRouterUsesLoadedEnvironmentForProjectMCP$/dev$/authenticated_tools_list$' -count=1` — exit 1. The Auth double was reached and returned the valid DEV identity; the test failed with `loaded BFF environment "" returned status 503, want 200: {"error":"OAuth resource authority unavailable"}`.
- Current GREEN source: implementation commit `f09c8e72b91d15ab8aa81ce0dd369330ad472bb0`. Command: `cd apps/bff && GOCACHE=/private/tmp/lwc438-gocache go test ./cmd/bff -run '^TestLWC438ProductionRouterUsesLoadedEnvironmentForProjectMCP$' -count=1` — exit 0. The full test covers all seven positive/negative cases for each of DEV, Prod, and local.
- Neighboring OAuth/security suites: `cd apps/bff && go test ./internal/config ./internal/authconsumer ./internal/handler/v1 ./internal/auth -run 'MCP|OAuth|BFFFile|AuthEnvironment' -count=1` — exit 0.
- `git diff --check` — exit 0.
- Test source SHA-256: `96ce0c6fc7a16119d4ddb63416085b5c9ebbbf85dbfea4fa46f1923a06c38e84`.
- Current `internal/config/bff_file.go` SHA-256: `f52a2fbb7aa9ceb8c45e2b7dda098c336c9c6fe4b911f427437a414588c115be`; historical `4d290cb7` file SHA-256: `9da1aa5f1e8e781fd871ad08e2b2c5219077f1fa15096a9d7d0b44430eacc5eb`.

The first historical overlay build referenced local-fixture constants introduced after `4d290cb7`; those test-only references were changed to their stable fixture literals. The final historical replay compiled and failed at the intended 503 assertion. The sandbox also denied the shared Go cache and loopback listener; the final GREEN and RED runs used the task-local Go cache and an authorized local test listener.

The canonical CI linkage is `.github/workflows/ci.yml`, job `bff`, step `Go tests` (`go test ./... -v -count=1 -race`). The new test is under `apps/bff/cmd/bff`, included by that command. Local verification covered the focused regression and neighboring OAuth packages; the complete CI job remains pending on the PR.

## DEV replay status and proposal

No live DEV or Production deployment was performed. The approved historical evidence identifies the old BFF image as `sha256:460eea1fca9f631eb1436617e48001f2bf9c4678e1bb61ec8a392884bb832f66`, with DEV revision `llm-wiki-bff-dev-00291-5ts` returning repeated 503s; Auth verification returned 200. This remains historical evidence, not a new live RED.

An isolated replay cannot safely target the current shared DEV service with today’s release configuration. `deploy/environments/development.yaml` fixes the BFF service to `llm-wiki-bff-dev`, and the deployment engine creates the BFF revision with no traffic before moving 100% of service traffic to that revision. Replaying old BFF code there would replace shared DEV traffic; there is no current isolated-service input or no-traffic replay operation.

Prerequisites for the dedicated deployment owner:

1. Provide or authorize a dedicated DEV BFF target and endpoint, plus a reviewed deployment-engine target configuration that the registered `Deploy Development` workflow can address. Do not change traffic on `llm-wiki-bff-dev`.
2. Confirm the DEV Auth verifier accepts the dedicated endpoint’s exact MCP resource origin and that the same project-scoped credential can be used against it. If the Auth resource allowlist or service identity needs changes, treat those as separate owner-approved mutations.
3. After this PR is reviewed and its candidate is on canonical `develop`, use the formal `.github/workflows/deploy-dev.yml` `release` operation with `components=bff` and `release_source_mode=legacy_monorepo`. Run a RED build from `source_sha=4d290cb7d2f5d9d8be9523f0ddc02b8ce1b621c9`, then a GREEN build from the exact reviewed candidate SHA. Use distinct owner-assigned explicit release tags; keep each run’s ready/result artifact and latest checkpoint.
4. Against the same dedicated endpoint, Auth environment, project resource, credential, and MCP request, capture each BFF revision, image digest, Auth verify status, BFF HTTP status, and MCP response. Expect Auth verify 200 for both, the old source’s BFF 503, and the candidate’s BFF 200 with `query_project` present in `tools/list`. Do not rollback or route either candidate to shared DEV traffic.

The current target configuration must be extended or an already-supported isolated target identified before those dispatches; the worker did not invent a target, tag, rollback, or candidate compatibility decision. Parent owns that decision, direct review, and all deployment/release actions.
