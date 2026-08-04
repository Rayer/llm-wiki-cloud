<!-- BEGIN:nextjs-agent-rules -->
# This is NOT the Next.js you know

This version has breaking changes — APIs, conventions, and file structure may all differ from your training data. Read the relevant guide in `node_modules/next/dist/docs/` before writing any code. Heed deprecation notices.
<!-- END:nextjs-agent-rules -->

## LWC Builder Handoff

LWC implementation starts only after product/architecture discussion has been consolidated into a YouTrack ticket and moved to `Submitted`.

When executing a submitted LWC ticket:

- Treat the ticket plus this repository context as the complete implementation package; do not reopen product discovery.
- **Do not load or invoke Superpowers.** Do not create a second spec or implementation plan.
- Load and follow the lightweight `karpathy-guidelines` skill: surface only genuine unresolved assumptions, make the minimum surgical change, and ensure every changed line traces to the ticket.
- Implement only the stated scope; treat non-goals as forbidden and avoid unrelated cleanup or speculative abstractions.
- Verify the ticket acceptance criteria and run its named regressions.
- Push the branch and open a non-draft PR; leave merge and deployment to the TPM/orchestrator.
