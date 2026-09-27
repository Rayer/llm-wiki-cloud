# LWC-344 DEV Export prerequisite provisioning

The Export Job has a separate, first-time provisioning workflow. It is restricted to `develop` and the fixed DEV contract in `deploy/provision/exportjob-dev.json`; it does not enable `export_job` in the normal deployment config or run the Job. The provisioning workflow is reusable only; invoke it through the already registered `deploy-dev.yml` entry workflow with the exact selector `components=provision-exportjob-dev`.

## Contract

- Project and region: `llm-wiki-cloud` / `asia-east1`.
- Job: `export-job-dev`, one task, parallelism 1, 23-hour task timeout, zero retries.
- Runtime identity: `lwc-export-worker-dev@llm-wiki-cloud.iam.gserviceaccount.com`.
- Signing identity: `lwc-export-signer-dev@llm-wiki-cloud.iam.gserviceaccount.com`.
- Image: built from the checked-out SHA using `apps/bff/Dockerfile.exportjob`; the Job uses the Artifact Registry digest.
- The workflow uses the existing DEV GitHub Actions workload identity configuration. It does not change workload identity, secrets, or credentials.

The runtime receives only the four fixed worker environment values: `GCP_PROJECT`, `BUCKET`, `FIRESTORE_DATABASE_ID`, and `EXPORT_SIGNING_SERVICE_ACCOUNT`. The configured signer identity stays fixed to the DEV signing service account.

## IAM added by the workflow

- BFF DEV identity: `roles/run.jobsExecutorWithOverrides` on `export-job-dev`, and the custom `lwcExportBlobSigner` role (only `iam.serviceAccounts.signBlob`) on the signing service account.
- Runtime DEV identity: `roles/datastore.user` conditioned on database `llm-wiki-cloud-dev`.
- Storage is bucket-bound on `llm-wiki-data-dev`: a custom list-only role grants `storage.objects.list`; a custom reader role grants only `storage.objects.get` conditioned on `users/`; a custom archive role grants `create`, `delete`, `get`, and `update` conditioned on `exports/tmp/` or `exports/ready/`.
- Signing identity: `lwc-export-signer-dev` gets only `storage.objects.get` on `exports/ready/`, in addition to the BFF's `iam.serviceAccounts.signBlob` grant. GCS authorizes a signed URL GET as the signer identity; the BFF and worker do not receive ready-archive reads through this binding. The signer role has no list, write, or source-object permission.
- The bucket must already have uniform bucket-level access. The workflow stops if it is disabled and never changes bucket configuration, lifecycle, or soft-delete.

Each custom role must already match its exact reviewed permission set if present. Resource creation is create-only; a mismatched existing resource stops the run. The workflow requires the successful `canonical-ci` job for exactly the dispatched SHA before gcloud setup, authentication, image build, or provider mutation, and shares a non-cancelling concurrency group with the DEV deploy lane. IAM changes require an etag, capture the complete policy before each set, retry only etag conflicts after a fresh read, preserve every prior binding, and verify exact bindings by read-back. The evidence journal is written before builds and creates; create results are recorded as accepted or unknown with read-back identities, and an ambiguous create remains ownership-unknown on rerun. The workflow restores the latest evidence artifact for the same SHA before provisioning; the script loads and preserves it rather than resetting resource ownership. Inspect the journal before retrying a partial run. The evidence artifact records resources and exact inverse bindings; resource deletion requires a separate consumer check.

## Operator runbook

1. Review and merge this source PR to `develop`; wait for canonical CI on the resulting full SHA.
2. From the repository root, dispatch the already registered wrapper on that exact `develop` SHA with this command: `gh workflow run deploy-dev.yml --ref develop -f components=provision-exportjob-dev`. The existing `components` string input is retained so GitHub's default-branch dispatch schema accepts the request; the selected `develop` revision routes only this exact value to the reusable provisioning workflow. The protected `Development` environment is set by the called workflow. Do not dispatch it on `main` or a tag, and do not dispatch `provision-exportjob-dev.yml` directly.
3. Inspect the workflow result and download `exportjob-dev-provision-<sha>-<run>-<attempt>`. Require `result=provisioned_and_read_back`, an immutable image digest, the exact resource list, etags, full before/after policies, and verified inverse binding records. A failed/partial run is recovered by dispatching the reviewed workflow again after resolving its reported mismatch; it never updates a mismatched existing Job or role.
4. Keep `deploy/environments/development.yaml` export disabled until the provisioning evidence is reviewed. Enabling it and deploying the BFF/Export Job are separate reviewed source and DEV deployment steps; this workflow does not run an Export execution or establish DEV acceptance.

The workflow needs the existing DEV deploy identity to build/push the image, create the two service accounts, the Job and custom role definitions, and set IAM on the exact target resources. If those existing permissions or bucket uniform access are absent, it stops; do not add WIF or broaden permissions as part of this lane.
