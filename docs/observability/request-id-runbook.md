# Request ID trace lookup

This runbook covers a read-only lookup from a server-issued request ID to matching Cloud Logging entries and the Cloud Trace records they identify. The trace begins at the owning service ingress: BFF requests at BFF ingress and Auth requests at Auth ingress. Browser rendering, navigation, CDN assets, and third-party pages are outside this lookup boundary.

## Run the local lookup

From the repository root, pass the server request ID copied from an actual response or log:

```sh
python3 scripts/trace_lookup.py 11111111-1111-4111-8111-111111111111
```

The defaults are environment `development`, project `llm-wiki-cloud` (overridable with `LWC_DEV_GCP_PROJECT`), and the last 30 minutes. Set a project and explicit RFC3339 time bounds for repeatable searches. Production requires both an explicit `--environment production` and `--project`:

```sh
python3 scripts/trace_lookup.py "$REQUEST_ID" \
  --environment development \
  --project llm-wiki-cloud \
  --since 2026-10-18T00:00:00Z \
  --until 2026-10-18T00:30:00Z \
  --output /tmp/request-trace.json
```

Use `--json` to write the same safe report to stdout instead of a file; `--json` and `--output` cannot be combined. Exit codes are `0` for complete results with traces, `1` for a completed search with no matching logs, `2` for invalid input, and `3` for incomplete results or an operational error. A no-data result means no matching retained entries were returned for that project and time window; it does not prove that the operation never happened.

The time window must be ordered and no longer than 31 days. The default lookup retries empty log searches and transient errors three times, with exponential delay. Cloud Logging pages are followed until `nextPageToken` is absent. Default bounds are 10,000 matched log entries, 1,000 pages, 250 distinct traces, and 10,000 spans per trace; passing any bound produces an incomplete/truncated status when it prevents full enumeration. Increase limits only when needed and keep the time window narrow. Do not treat a truncated result as a complete incident search.

The lookup reads logs with `logging.logEntries.list` and reads each identified trace with Cloud Trace v1 `projects.traces.get`. It never replays a product operation. `gcloud` must already have an approved active identity; the script asks `gcloud auth print-access-token` for an in-memory bearer token and does not print or persist it. The API calls use only read methods. For least privilege, a custom role can grant `logging.logEntries.list` and `cloudtrace.traces.get` on the target project. The predefined `roles/logging.viewer` and `roles/cloudtrace.user` include additional read or management permissions; use them only if approved for the operator. Trace Explorer scope selection can also require `cloudtrace.traceScopes.get`, `cloudtrace.traceScopes.list`, and `observability.scopes.get`; project selection can require `resourcemanager.projects.get`. Console pages can require these additional read permissions. No IAM grant or credential setup is performed by this tool.

## What the lookup searches

The tool searches log fields and text payloads for the input request ID, then extracts trace IDs only from entries where a supported request/origin/envelope field exactly matches that ID. The log field `request_id` and span label `request.id` are different evidence sources: logs discover trace IDs; each fetched trace is separately checked for direct `request.id`/`request_id` and origin request ID labels. A plain text mention without a request-ID field is not accepted as a match.

The equivalent Logs Explorer filter is below. Substitute the request UUID and the exact UTC bounds. Field-path components containing a period are quoted so `request.id` is treated as one key.

```text
timestamp >= "2026-10-18T00:00:00Z"
AND timestamp <= "2026-10-18T00:30:00Z"
AND (
  jsonPayload.request_id="11111111-1111-4111-8111-111111111111"
  OR jsonPayload."request.id"="11111111-1111-4111-8111-111111111111"
  OR jsonPayload."request.origin_id"="11111111-1111-4111-8111-111111111111"
  OR jsonPayload.origin_request_id="11111111-1111-4111-8111-111111111111"
  OR jsonPayload."origin_request.id"="11111111-1111-4111-8111-111111111111"
  OR jsonPayload.envelope_request_id="11111111-1111-4111-8111-111111111111"
  OR jsonPayload.Attributes.Value.Value="11111111-1111-4111-8111-111111111111"
  OR textPayload:"11111111-1111-4111-8111-111111111111"
)
```

The nested `jsonPayload.Attributes.Value.Value` clause covers the repository's pinned stdouttrace v1.44.0 `SpanStub` JSON shape (`Attributes[].Key/Value` and `SpanContext.TraceID`). The parser then requires an exact matching allowlisted request/origin attribute key and obtains the trace ID from `SpanContext.TraceID`. The `textPayload` clause is a substring search to find legacy text loggers and JSON text lines; it still requires an exact parsed request-ID field before it counts an entry. Logs Explorer's search language supports quoted special field components and `:` substring matching ([Logging query language](https://cloud.google.com/logging/docs/view/logging-query-language)). The equivalent programmatic enumeration uses `entries:list`, `pageSize`, and the returned `nextPageToken`; an empty `entries` array can still include a token and therefore does not mean enumeration is finished ([entries.list API](https://cloud.google.com/logging/docs/reference/v2/rest/v2/entries/list)).

The output deduplicates repeated log entries and trace IDs. Each trace is rendered separately; parent IDs form trees only within the same trace. Missing/invalid parents, cycles, missing or unrecognized finite outcomes, missing request attributes, duplicate span IDs, span-count bounds, and the 128-level tree-rendering bound are explicit issues. A separate trace is correlated as asynchronous work when the captured stdouttrace span Link points to the ingress trace or a matching span/log carries origin-request evidence; it remains a separate trace and is never shown as an invented parent. Attempt IDs are shown when available. If an HTTP terminal log reports status 202 but no related background trace evidence is visible, the result says `async_background.status=missing` and is incomplete. An accepted HTTP response, queue event, or poll is not a worker terminal result.

## Inspect traces in Cloud Console

Each returned trace has a Console URL with the project and trace ID. Open it in the intended project scope, or go to **Cloud Trace → Trace Explorer**, select the project and scope, and choose **Search for trace** in the toolbar; paste one 32-character trace ID. To filter by an attribute instead, choose **Add attribute filter**, set the key to `request.id` and the value to the request UUID; the filter bar displays `request.id: <UUID>`. This attribute filter only finds spans that actually contain that attribute. Cloud Logging filter text is not a Trace Explorer query. Inspect the trace tree, span attributes, and the Logs & Events tab. The Trace Explorer documents the Search for trace action and attribute filter syntax in [Find and explore traces](https://cloud.google.com/trace/docs/finding-traces).

For any reported async relationship, open both the origin trace and the related attempt trace. In the span details, open **Metadata & Links** and verify the stored Link points from the attempt to the actual origin trace/span. The lookup's origin ID/attempt correlation is not proof of a stored native Link. Record native Link verification as **MANUAL** until the Console shows the stored Link; same-request-ID logs, a bridge log, or two traces with matching IDs do not pass this check.

Cloud Trace v1 `GetTrace` returns the trace's spans and parent IDs, but its span schema has no Links field ([Trace v1 span schema](https://cloud.google.com/trace/docs/reference/v1/rest/v1/projects.traces)). The Trace v2 API currently exposes span creation/write methods (`createSpan` and `batchWrite`), not a read method for retrieving stored Links ([Trace v2 API methods](https://cloud.google.com/trace/docs/reference/v2/rest)). Consequently this CLI cannot verify native Links through its API response; Console inspection remains required.

## Read status and limits correctly

`found` with `complete: true` means the log pages were fully enumerated and all returned distinct traces/spans were readable without detected structural issues or an explicitly expected background trace being absent. `incomplete` means an API error, missing trace/attribute, malformed relationship, a configured bound, or missing background trace evidence after an HTTP 202 prevented a complete result. `not_found` means the bounded search found no matching logs. API failures are reported with finite categories such as `authentication_required`, `permission_denied`, `rate_limited`, `timeout`, `network_error`, `service_unavailable`, or `invalid_response`; response bodies and raw provider error text are omitted. Log lookup/export delays are visible as attempts such as `no_data` before `entries_found`; trace reads preserve retry outcomes per trace.

Cloud Trace sampling can omit spans before they reach the backend. Log export can lag the request, and log and trace export failures can differ. A trace may be in a different project than the Logs Explorer selection. Logging retention is normally 30 days in the `_Default` bucket (project retention can be configured from 1 to 3,650 days); `_Required` audit logs default to 400 days. Cloud Trace `_Trace` retention is 30 days. Expired data, excluded buckets/views, sampling, delayed export, or an incorrect project/window can all produce missing results; widen or shift the time bounds, verify the selected project/scope and permissions, and retry later when export may be delayed. Do not label missing data as a product failure or success. See [Logging retention and quotas](https://cloud.google.com/logging/quotas), [Cloud Trace retention and quotas](https://cloud.google.com/trace/docs/quotas), and [Trace troubleshooting](https://cloud.google.com/trace/docs/troubleshooting).

Cloud Trace read API quotas apply to the API lookup; 429 responses remain explicit and are retried only up to the configured attempt count. Trace Explorer UI reads use the selected project/scope and have their own access requirements. Read permission references: [Cloud Logging IAM](https://cloud.google.com/logging/docs/access-control) and [Cloud Trace IAM](https://cloud.google.com/trace/docs/iam).

## Output safety

Stdout, text output, and saved JSON contain only the supplied request ID, project/environment, time bounds, trace/span IDs, sanitized service/revision and operation/span names, allowlisted finite outcomes, attempt IDs, tree status, issue codes, and Console URLs. Raw log payloads, prompts, answers, tokens, authorization data, and complete resource/object paths are never emitted. Treat the saved JSON as operational metadata and store it only in an approved location.

## LWC-393–399 UAT mapping

This mapping says what the request-ID lookup can help an operator inspect. It does not complete each source ticket's implementation, full test matrix, live DEV case, or human UAT. All seven tickets remain separate evidence scopes; this checkpoint does not mark any as Verified.

| Ticket | Request-ID lookup evidence and UAT boundary |
| --- | --- |
| [LWC-393 — Query OTel](https://irisnode.youtrack.cloud/issue/LWC-393) | Start from the BFF server request ID, enumerate all matching traces, and inspect the Query root/phase parents, storage-read/validation outcomes, and real parallel LLM attempts when present. Cached reuse and unexecuted phases must not be inferred as network spans. This lookup does not prove Query response compatibility, storage guards, SDK exporter privacy, or Query-specific local integration cases. |
| [LWC-394 — Pipeline/Worker/quota OTel](https://irisnode.youtrack.cloud/issue/LWC-394) | Inspect actual submit/admission, status/readback, worker attempts, postprocess, and quota/recovery terminals by trace and attempt ID. A 202/accepted request remains separate from a pending or completed worker attempt. A cross-trace origin correlation still requires the manual stored-Link check above; this tool does not exercise worker, lease, publication, quota, or settlement behavior. |
| [LWC-395 — Auth/Profile/Export OTel](https://irisnode.youtrack.cloud/issue/LWC-395) | Begin Auth requests at Auth ingress; inspect actual Profile dispatch/derive/tag/compile/activation and Export admission/publication/state-commit traces as their own operations. Enqueue/redirect/start is not a background terminal or committed publication. This lookup does not prove those lifecycle transitions or their product-state semantics. |
| [LWC-396 — BFF data operations/Frontend correlation](https://irisnode.youtrack.cloud/issue/LWC-396) | Inspect actual non-Query BFF data-operation roots and their real read/mutation phases; compare a displayed diagnostic ID only with an ID actually supplied by the server. Missing IDs are not reconstructed. Browser rendering/navigation and frontend propagation require their own UI/API tests and are outside this worker's files and this lookup. |
| [LWC-397 — CLI observation/correlation](https://irisnode.youtrack.cloud/issue/LWC-397) | Use the lookup only when an existing server response/log provides a real request ID and matching remote trace evidence. Local scan/hash/diff/transfer summaries are not Cloud Trace spans, and this work does not add or require the optional CLI X-Request-ID extension. Existing CLI stdout, exit, partial, and unknown semantics need separate tests. |
| [LWC-398 — OTLP exporter/context propagation and six cases](https://irisnode.youtrack.cloud/issue/LWC-398) | A request ID can lead to trace IDs for a later exact backend readback; inspect same-trace parentage and cross-trace async relationships, then manually inspect native Links. The six cases require their own executed backend evidence. Sanitized fixtures and capture-only/local tests do not establish OTLP ingestion or a live product path. |
| [LWC-399 — All-entry request lifecycle](https://irisnode.youtrack.cloud/issue/LWC-399) | Starting with a true server request ID, enumerate the logs and all identified traces across sync and caused background attempts; inspect finite outcomes, attempts, and manual Link evidence. Auth starts at Auth ingress and BFF starts at BFF ingress. UI access/copy, MCP envelope/call identity, route inventory, and actual background terminal UAT are not proven by this CLI; browser rendering/navigation remains out of scope. |

For every ticket, retain the request ID and trace IDs, finite outcome, service/revision, issue/status codes, window, and manual Link result as evidence. Do not retain payloads or credentials. Record `NOT RUN` for any live DEV, backend ingestion, or Console check not actually executed.
