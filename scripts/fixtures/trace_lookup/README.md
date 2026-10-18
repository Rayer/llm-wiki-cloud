# Trace lookup SDK fixture provenance

`sanitized.json` retains the JSON object shape emitted by the repository-pinned
`go.opentelemetry.io/otel/exporters/stdout/stdouttrace` v1.44.0 exporter. The
source was generated offline through `observability.NewFilteredTraceProvider`,
`StartExportAttemptSpan(..., "export.worker")`, `StartWorkerSpan(...,
"pipeline.worker.export")`, `FinishExportSpan`, and `FinishPipelineSpan` in
`apps/bff/internal/observability/{otlp.go,propagation.go,tracing.go}`.

The worker root is an independent trace root with the producer-created Link to
the HTTP ingress trace; its child has a normal same-trace Parent. The structured
Cloud Logging entry keeps the parsed SDK object in `jsonPayload`, and the text
entry keeps the SDK JSON line in `textPayload`. Random trace/span IDs and
timestamps were replaced with stable synthetic values; the allowlisted request,
attempt, outcome, service, and revision values remain synthetic. No flat
`pipeline.worker.terminal` shortcut is used.

The emitted fields (`Name`, `SpanContext.TraceID`, `SpanContext.SpanID`,
`Parent`, `Attributes[].Key/Value`, `Links[].SpanContext`, and `Resource`)
match the pinned `tracetest.SpanStub` JSON serialization used by stdouttrace.
The dependency pin is `apps/bff/go.mod` and `apps/bff/go.sum`.
