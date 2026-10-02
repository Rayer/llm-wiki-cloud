---
name: deployment-operator
description: Operate this repository's LWC deployment engine through formal Actions entrypoints, inspect artifact receipts and typed breakpoints, and resume or recover retained releases. Excludes provisioning and data recovery.
---

Read [operator instructions](../../../docs/lwc-358/operator-r2.md) for exact inputs and result meanings.

Normal entry is Deploy Development or Promote Production with explicit components and release tag. Production container promotion requires the exact successful DEV result artifact ID. Preserve original build SHA separately from release SHA; never invent source mappings or semver.

Use the engine's result.json and latest target checkpoint. Runtime work belongs to the shared Actions workflow; do not operate providers directly or retry an unknown mutation from an older ready artifact. Correct the stated input/permission issue with the existing principal, or resume the exact checkpoint. A tag_failed result requires tag-only recovery. Selection enlargement uses a new plan and the retained artifact reference.

The temporary DEV Auth image diagnostic is exposed through the already registered Deploy Development workflow as `operation=diagnose-auth-image`; the default operation remains `release`. Both DEV and recovery diagnostic callers use the standalone, read-only `cd-auth-image-diagnostic.yml` reusable workflow, not the release workflow. Its source SHA is the selected, reviewed workflow-code SHA (`github.sha`), while the queried image target remains fixed to the failed release. It requires no checkpoint and does not create a ready receipt or release result. Use only the exact fixed payload and branch conditions in the operator instructions; the diagnostic job has no GH/Vercel runtime token and does not enter normal runtime.

A later serious issue requires the Owner's explicit recovery decision or an already-defined assertion. Invoke Recover retained deployment with affected components and the latest checkpoint; rollback and candidate reactivation perform no build. Preserve the successful source tag after rollback. Do not classify ordinary improvements as serious issues.

Stop at unknown state, failed recovery, stale/expired checkpoint, unrepresentable pre-state or scope ambiguity. Report the typed reason, checkpoint, redacted prior/candidate handles and safe next action to TPM. Do not substitute another credential, create/delete resources, reverse persistent writes or install this skill into another profile.

Release success means every selected component passes provider basic sanity and the explicit tag is recorded. Functional smoke/UAT is separate. Report only observed results; local implementation/tests do not authorize dispatch, tag publication, commits, push, PR, merge or live deployment.
