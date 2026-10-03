# Accepted r2 appendix — Frontend build-config output layouts

This appendix records the Owner-accepted clarification in 3584-1762 and 3584-1763. It supplements the frozen r2 specification; deployment-engine-spec-r2.md remains byte-for-byte unchanged.

Stage 1 accepts exactly one recognized representation below the configured Frontend .vercel/output directory:

1. **Static output:** static/build-config.json must be an existing regular file. Parse that exact file and require the existing exact schema and configured api_url and auth_url values.
2. **Official Vercel Prerender output:** use only the fixed same-key paths functions/build-config.json.func/, functions/build-config.json.prerender-config.json, and functions/build-config.json.prerender-fallback.body. The .func directory, .vc-config.json, descriptor, handler, and fallback must be present with the required file kinds and remain contained under the output root. The function config must name an existing safe relative handler that resolves inside this same .func directory. The descriptor must have type: Prerender; its fallback must have type: FileFsRef, fsPath: build-config.json.prerender-fallback.body, and contentType: application/json; initialHeaders["content-type"] must also be application/json. Parse only that exact fallback body and apply the same exact schema and configured-target comparison used for Static output.

Missing, malformed, unsupported, escaping, mismatched, or wrong-target representations fail closed. A Static file together with any exact Prerender marker is ambiguous and fails closed. The engine does not scan the output tree, accept an arbitrary body, infer a route from another file, or read Vercel environment values from the function config. This check does not add a route-table gate; the outer .vercel/output/config.json is outside the pinned writer probe.

Prepare and the existing best-effort DEV diagnostic call the same local layout validator. The diagnostic keeps its existing fixed fields and booleans; it adds no layout or descriptor detail. Runtime provider observation continues to compare Vercel's logical build-config.json response with the immutable artifact target; runtime arguments, aliases, provider sanity, archive contents, restored .vercel/project.json, Auth receipt reuse, and the all-selected ready barrier are unchanged.

The source probe and filtered output under evidence/dev-37111842689-prerender-chain-* record the real Next 16.2.7 → pinned @vercel/next 11.0.2 → vercel 59.11.7 writer serialization. The incidental function runtime value is not part of validation. The historical DEV prepare failure's runner layout and cause remain unknown; this local compatibility repair does not claim to diagnose that past run.
