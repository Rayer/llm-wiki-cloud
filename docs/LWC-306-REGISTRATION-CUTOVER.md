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
3. Capture current rollback refs and provider authority handles.
4. Bootstrap-register `main` at the exact merged `develop` SHA. This registers
   the `workflow_dispatch` workflows and must not deploy or mutate a provider.
5. Read back `main == develop`, the root workflow inventory, and zero provider
   mutations.
6. Only after those checks, configure the approved environments, secrets,
   bindings, and protection/provider integration. Later branch protection must
   trust only the DEV-readiness producer in `deploy-bff.yml`, whose status is
   published after exact-SHA DEV deployment and promotion-readiness gates.

This checklist is the bootstrap evidence required before the later
`lwc-deployer` topology update.
