# Export archive lifecycle

The reviewed GCS rule source is [`deploy/storage/export-lifecycle.json`](../../deploy/storage/export-lifecycle.json). It deletes finalized objects under `exports/ready/` after three days from their GCS `Custom-Time`, and finalized objects under `exports/tmp/` after one day by object age. The export contract sets `Custom-Time` only after the complete ZIP is written and validated; `snapshot_at` is unrelated to retention. Only ready objects can be signed, and the API rejects downloads once `Custom-Time + 72h` has passed, regardless of when GCS processes deletion.

The temporary rule relies on the LWC-344 contract that the export job's hard timeout is below 24 hours and its active/recoverable-job behavior is tested. This policy only sees finalized objects: unfinished uploads are not proof of an object that this rule can clean up. A job exceeding its approved lifetime would invalidate the one-day safety premise.

## Safe merge

GCS bucket lifecycle updates replace the bucket's lifecycle configuration. Do not apply the policy file as a complete replacement. First obtain the current bucket lifecycle configuration through the separately authorized deployment process, save its `lifecycle` object as JSON shaped like `{"rule": [...]}`, then produce a candidate:

```sh
python3 scripts/merge_export_lifecycle.py --existing /path/to/current-lifecycle.json > /path/to/candidate-lifecycle.json
```

The merger preserves every existing rule, adds only the two approved export rules, and is idempotent. It fails closed if an existing Delete rule has no prefix restriction or can match either export prefix, and it rejects duplicate export rules; review and resolve such a conflict before producing a candidate. It never reads cloud credentials or applies a provider change. Review the complete candidate against the current config before a separately authorized update.

## Deletion and bucket protection caveats

The live bucket's soft-delete, Object Versioning, and retention policy settings have not been read in this task and remain unknown. GCS lifecycle actions are asynchronous and do not guarantee deletion exactly at the eligibility instant. With soft delete enabled, lifecycle-deleted objects remain recoverable and billable through that retention period; GCS defaults to seven days for supported buckets unless configured otherwise. With Object Versioning enabled, deleting a live version makes it noncurrent; this policy does not define a separate noncurrent-version retention window. Existing retention policies or holds can delay lifecycle deletion. This policy does not disable or alter those bucket protections. Therefore the three-day application expiry is a download boundary, not a promise of physical erasure or a bound on storage cost. See [GCS Object Lifecycle Management](https://cloud.google.com/storage/docs/lifecycle) and [soft delete](https://cloud.google.com/storage/docs/soft-delete) for provider behavior.

The tests verify the JSON policy and deterministic merge/eligibility behavior only. They do not demonstrate that GCS applied the policy or physically deleted any object.
