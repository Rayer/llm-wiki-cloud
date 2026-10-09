# LWC-336 accepted implementation notes

These owner-accepted notes refine the frozen MCP implementation contract recorded for LWC-336. They make transport and test details explicit without changing the LWC-375 project-key policy.

1. **Bind each tool call to the current request identity.** Project-key authentication runs before the MCP transport for every request. The verified user, project, and key ID are placed in the request context that reaches the actual tool handler. MCP session metadata is not identity; a later request with another valid key must use that key's current project scope.
2. **Keep SDK/session identity from crossing keys.** The adapter uses a stateless Streamable HTTP handler. It does not issue or retain `Mcp-Session-Id`, so it cannot reuse a previous key's user/project identity. The current account, key state, and project-owner authority are checked on each request.
3. **Separate Query domain outcomes from operational errors.** Empty results, insufficient evidence, and model-prior disclosure are ordinary Query responses. Actual executor, profile, or storage failures return `isError: true` and bounded safe text. A test fixture's `execution_error` result is not treated as a normal product Query response.
4. **Share the existing Query boundary.** HTTP and MCP use the same active Profile-generation resolver, Query executor, `mapQueryResult`, and `QueryResponse.MarshalJSON`. This retains HTTP `required_tag_ids` behavior, profile generation pinning, existing runtime-identity headers on HTTP, and the explicit empty `citations: []` model-prior JSON case.

Review `6750ffceada0474ca8ade5f4` is PASS for the concrete LWC-336 design contract. It is not product implementation or product acceptance. The separate LWC-375 contract review `cb79f1ab7e71989471d0bf9e` and earlier Spike-scope review `dfeb023d714ebe33a9a61d09` retain the distinct scopes recorded in the LWC-377 report.
