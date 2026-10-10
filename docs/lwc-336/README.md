# LWC-336 Hermes MCP setup

LWC-336 adds a read-only Streamable HTTP MCP endpoint to the existing BFF. The service uses `github.com/modelcontextprotocol/go-sdk/mcp` v1.8.0 and exposes one tool, `query_project`, at `/mcp`. The tool calls the same project-scoped Query consumer and `QueryResponse` projection as `POST /api/v1/query`.

## Access and scope

Create a project key in the Web app under **Project Settings → External access → Project API keys** for the selected Project. The route remains /profile and the existing Profile editor remains on that page. The key is fixed to that owner and Project and grants only Query access. It cannot select another Project, request required_tag_ids, or call profile, detail, write, admin, or Auth operations. Multi-Project keys are not supported.

The full key appears only once in the successful create result. Check `hermes profile list` and use the active profile marked with *. Copy the key directly into that active profile's .env secret source as `LWC_PROJECT_KEY`; the default profile uses `~/.hermes/.env`. Use a secure local editor or your configured secret source, limit file access to your user, and never put the key in a command argument, `config.yaml`, chat, prompt, URL, repository, log, or browser storage.

The MCP configuration stores only a variable reference. Copy the MCP URL displayed in Project Settings, built from this Web app's runtime BFF API URL plus /mcp; do not guess a hostname or use a URL from a different environment.

```yaml
mcp_servers:
  llm-wiki:
    url: "<copy the MCP endpoint shown in Project Settings>"
    transport: streamable-http
    headers:
      Authorization: "Bearer ${LWC_PROJECT_KEY}"
    sampling:
      enabled: false
```

Restart Hermes and let it discover the server's MCP tool. Use the tool name shown by discovery; generated wrapper names depend on the configured server name. See the official [Hermes MCP documentation](https://hermes-agent.nousresearch.com/docs/user-guide/features/mcp/) and [secrets and profiles guide](https://hermes-agent.nousresearch.com/docs/user-guide/secrets/).

This is client configuration; the BFF does not add an app setting or put dynamic key records in CaC. The key is rechecked against current key, account, and Project-owner state for every HTTP request.

## Use and rotation

Hermes discovers the query_project tool and passes q plus optional mode (wiki or full, default wiki). Results keep the HTTP Query JSON fields: query, mode, results, and any present expand, ai_synth, citations, status, reason, answer_basis, wiki_evidence_status, and disclosure_required. Empty/insufficient results and model-prior disclosure are normal Query results; they are not MCP tool errors. Executor or storage failures return an MCP tool error with safe text. Discovery and a successful MCP call do not prove that the Project has a published generation or that Query found evidence; check the Query response and published generation separately.

There is no refresh operation or fixed expiry. Rotate a key by creating a replacement, updating the active Hermes profile's secret value, restarting Hermes and verifying the new connection, then revoking the old key under Project Settings. Revocation takes effect on the next request; a previous MCP session header does not preserve authority.

Grok web interface support for this key is unverified; this guide does not provide Grok web setup steps.

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
