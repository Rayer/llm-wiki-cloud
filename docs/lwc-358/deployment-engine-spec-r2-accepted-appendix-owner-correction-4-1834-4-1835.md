# Accepted r2 appendix — readback and same-attempt checkpoint ordering

This appendix records Owner directions 4-1834 and 4-1835. It supplements the frozen r2 specification. The earlier accepted 4-1777 appendix remains byte-for-byte unchanged; this correction supersedes only its highest-artifact-ID selection statement for artifacts within one attempt.

Within the same target, plan prefix, workflow run, and attempt, select the checkpoint with the greatest sequence; artifact ID is the download locator, not the sequence order. Selection between attempts remains unchanged. Complete listing, workflow trust, expiry handling, and the existing mutation checks remain in force.

`readback` remains Actions-owned and validates/uses its retained plan and component receipt. Because it only polls provider state, it bypasses the latest-checkpoint mutation/stale guard. It performs the existing provider read and writes the existing result, but does not save a checkpoint or issue a provider write. Mutating operations continue to use the latest-checkpoint guard.
