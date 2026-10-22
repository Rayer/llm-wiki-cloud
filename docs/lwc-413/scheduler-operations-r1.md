# LWC-413 scheduler operations — r1

**Contract:** `daily-incremental-r1`
**Schedule authority:** `deploy/cac/ssot.pkl` (`pipeline_schedule`)
**Entry point:** `POST /internal/pipeline/schedule`
**Scope:** operational procedure for TPM after the source release is qualified; this document does not create or enable a cloud resource.

## Configuration and identity

The generated schedule is hourly at minute zero in UTC (`0 * * * *`, `UTC`). Local config keeps both OIDC identity fields empty, so the scheduled route stays unavailable locally. DEV and Production use the matching BFF Profile runtime service URL as the OIDC audience and the existing BFF runtime service account as the token identity:

| Environment | BFF origin and OIDC audience | OIDC service account | Target URI |
|---|---|---|---|
| DEV | `https://llm-wiki-bff-dev-a5nkmux6pq-de.a.run.app` | `lwc-bff-dev@llm-wiki-cloud.iam.gserviceaccount.com` | `https://llm-wiki-bff-dev-a5nkmux6pq-de.a.run.app/internal/pipeline/schedule` |
| Production | `https://llm-wiki-bff-a5nkmux6pq-de.a.run.app` | `lwc-bff-prod@llm-wiki-cloud.iam.gserviceaccount.com` | `https://llm-wiki-bff-a5nkmux6pq-de.a.run.app/internal/pipeline/schedule` |

The route accepts POST only. It verifies a Google ID token with an accepted Google issuer, the exact configured audience, the exact service account email, and `email_verified=true` before scanning. The request body carries no owner, Project, trigger, or bypass authority. Do not add a test-Project query parameter or restrict the sweep: every eligible Project is examined, and any Project with pending eligible work can receive its daily scheduled attempt.

The BFF status reports availability from its loaded schedule and identity configuration; it does not read Cloud Scheduler job state. Because Prod CaC contains the Prod identity before the Prod BFF config is deployed, stage the Prod job in `PAUSED` state before deploying that config. Deploy and validate DEV first. After DEV acceptance, stage Prod paused, deploy the qualified Prod config, verify it, then resume only at the approved Production enablement step. A paused or absent job can still be displayed as configured/available after the BFF config deploy; the runbook ordering closes the absent-job window, while the status remains config availability rather than a Scheduler-state signal.

The configured OIDC accounts are existing BFF runtime identities from CaC; this change creates no service account or IAM binding. Before creation, TPM must read back that the selected account can be used by Cloud Scheduler and invoke that environment's Cloud Run service. If the existing permissions do not satisfy those prerequisites, hold activation for separately authorized IAM work.

## Create and stage a job

Use the matching environment row above. The commands below are examples for the existing Google Cloud project and region. Choose exactly one environment and keep `OIDC_AUDIENCE` equal to its origin without a path or query string.

```sh
PROJECT=llm-wiki-cloud
REGION=asia-east1
SCHEDULE='0 * * * *'
ENV=dev
case "$ENV" in
  dev)
    BFF_ORIGIN=https://llm-wiki-bff-dev-a5nkmux6pq-de.a.run.app
    OIDC_SERVICE_ACCOUNT=lwc-bff-dev@llm-wiki-cloud.iam.gserviceaccount.com
    JOB=lwc413-incremental-hourly-dev
    ;;
  prod)
    BFF_ORIGIN=https://llm-wiki-bff-a5nkmux6pq-de.a.run.app
    OIDC_SERVICE_ACCOUNT=lwc-bff-prod@llm-wiki-cloud.iam.gserviceaccount.com
    JOB=lwc413-incremental-hourly-prod
    ;;
  *) echo "ENV must be dev or prod" >&2; exit 2 ;;
esac
TARGET_URI="${BFF_ORIGIN}/internal/pipeline/schedule"
OIDC_AUDIENCE="$BFF_ORIGIN"
```

First confirm that the selected job name is unused and inspect the target's current Cloud Run URL and IAM policy read-only. The current `gcloud scheduler jobs create http` interface does not offer a create-paused flag. In this release window (October 2026), create with a future UTC staging schedule, pause immediately, read back `PAUSED`, then update the job to the authoritative hourly schedule while it remains paused. If staging occurs after 2026, select a future staging date far enough out to complete the pause and readback before it is due.

```sh
gcloud scheduler jobs create http "$JOB" \
  --project="$PROJECT" --location="$REGION" \
  --schedule='0 0 1 1 *' --time-zone=UTC \
  --uri="$TARGET_URI" --http-method=POST \
  --oidc-service-account-email="$OIDC_SERVICE_ACCOUNT" \
  --oidc-token-audience="$OIDC_AUDIENCE" \
  --attempt-deadline=30m --max-retry-attempts=0 \
  --description='LWC-413 scheduled daily incremental Project sweep'

gcloud scheduler jobs pause "$JOB" --project="$PROJECT" --location="$REGION"
gcloud scheduler jobs describe "$JOB" --project="$PROJECT" --location="$REGION" --format=json

gcloud scheduler jobs update http "$JOB" \
  --project="$PROJECT" --location="$REGION" \
  --schedule="$SCHEDULE" --time-zone=UTC \
  --uri="$TARGET_URI" --http-method=POST \
  --oidc-service-account-email="$OIDC_SERVICE_ACCOUNT" \
  --oidc-token-audience="$OIDC_AUDIENCE" \
  --attempt-deadline=30m --max-retry-attempts=0

gcloud scheduler jobs describe "$JOB" --project="$PROJECT" --location="$REGION" --format=json
```

Confirm `state: PAUSED`, `schedule: 0 * * * *`, `timeZone: UTC`, the path, method, audience, account, and `retryConfig.retryCount: 0`. The 30-minute deadline is the supported HTTP-target maximum and gives the synchronous paginated sweep its longest supported request window; it does not guarantee a very large sweep will finish. Scheduler retry attempts remain zero because a timed-out or lost response can follow a durable reservation; an automatic retry could obscure whether work was accepted. A later hourly tick re-enters the shared admission seam, which preserves the once-per-Project-per-UTC-day entitlement and unresolved execution ownership.

After the BFF config deploy, make a no-token refusal check before resuming. Use the exact environment URI; do not include an Authorization header or token:

```sh
curl --silent --show-error --include --request POST "$TARGET_URI"
```

With the environment identity configured, expect an application `401`; an earlier Cloud Run IAM rejection such as `403` is also a refusal. Confirm no execution or provider invocation was created. A local config with empty identity instead returns `503` before token validation, which is the intentional local-disabled behavior. Never send the real Scheduler token in the manual refusal check.

## DEV causal acceptance

Perform this only after the DEV BFF config is deployed and the job readback matches the DEV row. Use one existing, owned, ready, non-Demo DEV Project and its ordinary owner account. Do not seed Firestore, set a fake clock, modify quota/budget/receipt documents, or use another profile's data.

1. Capture the Project's current published generation, pending counts, last scheduled execution/attempt, and ordinary manual `runs_today` from the owner-scoped status UI/API. Check the current DEV sweep scope with the owner; the job has no Project filter and can process any eligible Project with pending work.
2. Add a unique raw article through the existing supported upload UI/API. Do not start a manual pipeline. Confirm that the new raw is pending while the previously published generation is still served.
3. Resume the DEV job and wait for the next UTC hourly tick. For an immediate controlled invocation, `gcloud scheduler jobs run "$JOB" --project="$PROJECT" --location="$REGION"` sends the same authenticated whole-Project sweep; use it only after the owner accepts that scope. Do not call the internal route with a fabricated token.
4. Read back the same Project's scheduled attempt and exact execution ID. Verify a real Cloud Run worker execution, consumed-source receipt for the uploaded raw, a committed publication generation, and owner-scoped status/source readback. A queued request or Cloud Run job exit alone is not publication evidence.
5. Query a distinctive phrase from the new raw in the normal DEV Query UI/API. Verify the answer and canonical citation point to the newly published content in that same Project and generation.
6. After the first attempt settles, run the same Scheduler job a second time within the same UTC day to test replay protection. Compare the scheduled execution/invocation counter and exact daily reservation before and after: the replay must not create another invocation for this Project. Confirm ordinary manual `runs_today` is unchanged throughout; do not use a manual run as the control.
7. Record source revision, UTC timestamps, Project/execution/generation/citation IDs, scheduler readbacks, relevant status/source/Query receipts, and sanitized logs. Do not copy tokens, raw article bodies, or private URLs into the record.

`gcloud scheduler jobs run` is a project-wide sweep, not a Project-specific test trigger. It can act on other eligible Projects with pending input. The DEV test operator must account for that scope before every resume or manual Scheduler run. Same-day replay checks must verify the target Project's invocation counter, because unrelated Projects may legitimately receive their own first daily attempts in that sweep.

## Production enablement

Production follows successful DEV verification on the same qualified source SHA and the Owner/TPM release gate. Before Prod BFF config deployment, create/read back the Prod job in `PAUSED` state with the Production identity row and the canonical hourly settings. Deploy Prod config only after DEV is accepted, run the no-token refusal check, verify the paused job fields, then resume at the separately approved enablement step. Confirm the first Prod scheduled run with production status, execution, consumed receipts, publication, and Query citation readback using an owned Production Project; this worker made no such request or write.

## Pause and recover

Pause immediately if the target, account, audience, UTC schedule, retry setting, Project-wide scope, unauthorized-call response, execution evidence, or published result differs from the reviewed expectation:

```sh
gcloud scheduler jobs pause "$JOB" --project="$PROJECT" --location="$REGION"
gcloud scheduler jobs describe "$JOB" --project="$PROJECT" --location="$REGION" --format=json
```

Pausing stops future scheduled requests; it does not cancel an already-running HTTP request. Read the exact execution/reservation and publication evidence before deciding whether a sweep is settled. On timeout, a lost response, or an unknown execution outcome, keep the job paused for investigation and do not trigger an immediate retry; the durable same-day claim is intended to prevent another invocation. Resume only after the cause and current Project-wide sweep scope are understood. If the BFF must become disabled as well, reverting the environment's identity in CaC and deploying that config is a separate release action; pausing the job alone does not change config-derived UI availability.

## References and non-actions

- [Cloud Scheduler `gcloud scheduler jobs create http` reference](https://docs.cloud.google.com/sdk/gcloud/reference/scheduler/jobs/create/http) documents HTTP method, OIDC account/audience, deadline and retry flags; it has no create-paused option.
- [Cloud Scheduler HTTP target authentication](https://docs.cloud.google.com/scheduler/docs/http-target-auth) documents OIDC token creation and service-agent requirements.
- [Cloud Scheduler job creation, pause, and resume](https://docs.cloud.google.com/scheduler/docs/creating) documents staging and later job pause/resume operations.
- [Cloud Scheduler job API](https://docs.cloud.google.com/scheduler/docs/reference/rest/v1/projects.locations.jobs) documents the 15-second to 30-minute HTTP deadline range and retry behavior.

No Cloud Scheduler job, IAM binding, service account, credential, BFF deployment, provider invocation, or live Project data was changed while preparing this runbook.
