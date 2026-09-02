# LWC-306 workflow registration cutover

This is a post-merge operator gate. Phase 3 does not perform any of these
actions, dispatch a workflow, or change provider/GitHub configuration.

1. Merge the deployment-workflow PR. Confirm the exact merge SHA passes root
   canonical CI on `develop` and receives `main-fast-forward-eligible=success`.
2. Run repository/provider preflight. Confirm the new repository is not yet
   connected to a production Git integration and no push-triggered workflow
   mutates a provider.
3. Capture current rollback refs and provider authority handles.
4. Fast-forward `main` to the exact eligible `develop` SHA. This registers the
   `workflow_dispatch` workflows and must not deploy or mutate a provider.
5. Read back `main == develop`, the root workflow inventory, and zero provider
   mutations.
6. Only after those checks, configure the approved environments, secrets,
   bindings, and begin the DEV deployment.

This checklist is the bootstrap evidence required before the later
`lwc-deployer` topology update.
