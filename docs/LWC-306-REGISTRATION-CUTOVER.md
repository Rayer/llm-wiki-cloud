# LWC-306 workflow registration cutover

This is a post-merge operator gate. Bootstrap `main` registration occurs before
branch-protection or provider integration. Phase 3 does not perform any of
these actions, dispatch a workflow, or change provider/GitHub configuration.

1. Merge the deployment-workflow PR. Confirm the exact merge SHA passes root
   canonical CI on `develop`; canonical CI is tests/aggregate only and does not
   publish `main-fast-forward-eligible`.
2. Run repository/provider preflight. Confirm the new repository is not yet
   connected to a production Git integration and no push-triggered workflow
   mutates a provider.
3. Capture current rollback refs and provider authority handles. Treat any
   Vercel root/link correction as rollback-first: record the current project
   metadata and deployment/alias handles, make the smallest approved change,
   and independently read back the exact state before proceeding. The
   production project must use root directory `apps/frontend`, be linked to
   `Rayer/llm-wiki-cloud`, and identify `main`; the DEV project must use the
   same root directory, while its explicit numeric GitHub repository-ID
   fallback may remain when the project is intentionally unlinked.
4. Bootstrap-register `main` at the exact merged `develop` SHA. This registers
   the `workflow_dispatch` workflows and must not deploy or mutate a provider.
5. Read back `main == develop`, the root workflow inventory, and zero provider
   mutations.
6. Only after those checks, configure the approved environments, secrets,
   bindings, and protection/provider integration. Before enabling the Workload
   Identity Federation (WIF) path, bind the new-repository principal to the
   exact repository identity and confirm the GitHub environment secret names
   required by the workflows are present and correctly scoped. Record readiness
   only; do not place token, project, team, repository, or secret values in
   this document.
7. Later branch protection must
   trust only the DEV-readiness producer in `deploy-bff.yml`, whose status is
   published after exact-SHA DEV deployment and promotion-readiness gates.

This checklist is the bootstrap evidence required before the later
`lwc-deployer` topology update.
