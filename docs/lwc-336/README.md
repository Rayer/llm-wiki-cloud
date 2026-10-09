# LWC-336 Hermes MCP setup

LWC-336 adds a read-only Streamable HTTP MCP endpoint to the existing BFF. The service uses `github.com/modelcontextprotocol/go-sdk/mcp` v1.8.0 and exposes one tool, `query_project`, at `/mcp`. The tool calls the same project-scoped Query consumer and `QueryResponse` projection as `POST /api/v1/query`.

## Access and scope

Create a project key in the Web app under **Account Settings → Project API keys** for the workspace's current project. The key is fixed to that owner and project and grants only Query access. It cannot select another project, request `required_tag_ids`, or call profile, detail, write, admin, or Auth operations.

The full key appears only in the successful create result. Copy it into the Hermes profile's environment or secret source under `LWC_PROJECT_KEY`; do not save the value in `config.yaml`, a prompt, a URL, repository file, log, or browser storage. The MCP configuration stores only the variable reference:

```yaml
mcp_servers:
  project-key:
    url: https://<bff-origin>/mcp
    transport: streamable-http
    headers:
      Authorization: "Bearer ${LWC_PROJECT_KEY}"
    sampling:
      enabled: false
```

Keep the key in Hermes' active profile secret source or environment. This is client configuration; the BFF does not add an app setting or put dynamic key records in CaC. The key is rechecked against current key, account, and project-owner state for every HTTP request.

## Use and rotation

Hermes discovers `query_project` and passes `q` plus optional `mode` (`wiki` or `full`, default `wiki`). Results keep the HTTP Query JSON fields: `query`, `mode`, `results`, and any present `expand`, `ai_synth`, `citations`, `status`, `reason`, `answer_basis`, `wiki_evidence_status`, and `disclosure_required`. Empty/insufficient results and model-prior disclosure are normal Query results; they are not MCP tool errors. Executor or storage failures return an MCP tool error with safe text.

There is no refresh operation or fixed expiry. Rotate a key by creating a replacement, updating the Hermes secret value, confirming a query succeeds, then revoking the old key in Account Settings. Revocation takes effect on the next request; a previous MCP session header does not preserve authority.

For a direct Query API client, use the same key and existing HTTP endpoint:

```sh
curl --fail-with-body --request POST "${BFF_ORIGIN}/api/v1/query" \
  --header "Authorization: Bearer ${LWC_PROJECT_KEY}" \
  --header "Content-Type: application/json" \
  --data '{"q":"coffee","mode":"wiki"}'
```

`X-Project-ID` is optional; if provided, it must equal the project bound to the key. A project key is not a CLI access or refresh token.

## Transport behavior

The endpoint uses the SDK's stateless Streamable HTTP handler with JSON responses. It negotiates the MCP protocol version with the client; the local Hermes acceptance run negotiated `2025-11-25`. The BFF does not issue `Mcp-Session-Id`; each tool call is authenticated and scoped from its own current request. GET and DELETE requests are authenticated first and then return `405` because the adapter is stateless; POST initialize, tool-list, and tool-call requests use `application/json` and return JSON.

Authentication failures are `401` for missing, malformed, unknown, wrong-secret, or revoked keys; `403` for a mismatched project header, suspended account, or lost project ownership; and `503` when current authority storage is unavailable. Other protected BFF routes keep their existing JWT boundary. Public `/api/v1/auth/login`, `/register`, `/refresh`, `/logout`, and `GET /api/v1/query/config` remain as they are.

## Local acceptance harness

The owned harness builds the actual BFF router, creates synthetic owner/project/key records in a loopback Firestore emulator, and connects the native Hermes registry using an isolated `HOME` and `HERMES_HOME`. It sends disposable credentials only through process memory/pipe, disables sampling, makes no model calls, and saves only sanitized evidence under `docs/lwc-336/evidence/native/`.

Start the Firestore emulator on loopback and run:

```sh
cd apps/bff
PATH="/opt/homebrew/opt/openjdk@21/bin:$PATH" \
  gcloud emulators firestore start --host-port=127.0.0.1:8596 --project=lwc336-local-contract
```

In a second terminal at the repository root:

```sh
LWC336_EMULATOR_HOST=127.0.0.1:8596 docs/lwc-336/harness/run.sh
```

The runner removes its uniquely named `lwc336.*` scratch child on exit. It does not read, alter, or use a real Hermes profile secret. `report.md` records the exact run, protocol version, source hashes, tests, skipped emulator cases, and limits; the harness does not establish live cloud or paid-provider acceptance.
