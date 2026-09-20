# Public test corpora

The recommended positive smoke is `raw-dns/reserved-dns.md` plus
`cases-dns.jsonl` and sealed base `query-dns.json`. The source is an explicitly
labeled paraphrase of public [RFC 2606](https://www.rfc-editor.org/rfc/rfc2606.txt),
not a private corpus or a full normative copy. The two queries concern reserved
DNS testing and documentation names.

Use **both** `--query-profile corpus-derived-tech-document-v1` and
`--query-prompt domain-neutral-technical-v1`. These select existing production
support and create an exact generation/concepts binding in the run's effective
config. The base config preserves the required lifestyle default; using it
without the explicit binding is not the documented DNS baseline. The sealed fixture retains the historical query aliases
`deepseek-v4-flash` and `deepseek-v4-pro`; the current production client resolves
both to `deepseek-flash`. Synto also defaults to `deepseek-flash`. `--require-grounded` is mandatory for the positive smoke.
The sealed base uses the existing typed no-evidence terminal policy.

The original Alice source and cases below remain available as historical test
data. Bare names with the lifestyle default failed parent live verification and
must not be described as a validated positive baseline.

`raw/alice.md` contains a short public-domain excerpt from Lewis Carroll's
*Alice's Adventures in Wonderland* (1865), Chapter I, sourced from
https://www.gutenberg.org/files/11/11-h/11-h.htm (accessed 2026-09-19).
The excerpt is unmodified apart from wrapping and the explicitly labeled test
header/frontmatter. It contains no private user data. `cases.jsonl` contains two
small wiki-mode identity queries; generated concept slugs are intentionally not
assumed in advance.

Tests that say `MOCK` or `explicit_mock` are offline provider/orchestration test
outputs. They are not live model evidence. Usage and verification limits:
[local staged experiments](../../../docs/local-staged-e2e.md).
