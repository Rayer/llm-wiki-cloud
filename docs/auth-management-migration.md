# Auth management scope migration

`cmd/auth_management_migrate` converts the legacy `users.role == "admin"` source into the registered `auth:admin` and `lwc:admin` scope states. It is an operator tool; there is no application bootstrap endpoint or automatic role-to-scope fallback after a target has a migration marker.

## Preview and apply

Run preview against the selected Auth configuration and save its JSON output as the immutable manifest for review. Preview reads the current admin accounts, records each account document's exact Firestore update time and status, and proposes both registered scopes. Preview does not write Firestore.

```sh
LWC_APP_CONFIG_PATH=/absolute/path/to/auth.json go run ./cmd/auth_management_migrate \
  -mode preview -run-id auth-admin-dev-2026-10-27
```

Review the complete target list and source environment before a separately authorized apply. Apply takes only an explicit schema-1 manifest. For every scope it transactionally rechecks the account role, status, update time, existing principal, scope lock, and idempotency record before writing. Changed, inactive, deleted, already-granted, previously processed, and revoked targets are skipped; a revoked state is never replaced by a migration grant. Reuse the same run ID and exact manifest to retry an uncertain operation.

```sh
LWC_APP_CONFIG_PATH=/absolute/path/to/auth.json go run ./cmd/auth_management_migrate \
  -mode apply -manifest /absolute/path/to/reviewed-manifest.json
```

Nonlocal apply requires both a separately authorized execution and an explicit `-authorize-live-target` value equal to the loaded Auth environment. This implementation task does not authorize or perform a live apply.

## Readback

Readback fetches the durable result for each target and scope by run ID; it never resubmits mutations.

```sh
LWC_APP_CONFIG_PATH=/absolute/path/to/auth.json go run ./cmd/auth_management_migrate \
  -mode readback -manifest /absolute/path/to/reviewed-manifest.json
```

Compare every readback row with the reviewed manifest. `applied` means a new grant committed. `skipped` includes a reason and does not imply that the account was activated or that a scope was granted.

## Deployment order and recovery

The safe DEV sequence requires a separate parent authorization before any nonlocal mutation. The flag is an environment guard, not that authorization. Use the generated DEV Auth config and keep its contents out of logs and reports:

```sh
LWC_APP_CONFIG_PATH="$AUTH_DEV_CONFIG" go run ./cmd/auth_management_migrate \
  -mode initialize-shadow -authorize-live-target dev

# Deploy the Auth and BFF dual-read candidates, then route all first-party
# account and admin mutations through Auth and stop old role writers.
LWC_APP_CONFIG_PATH="$AUTH_DEV_CONFIG" go run ./cmd/auth_management_migrate \
  -mode preview -run-id auth-admin-dev-2026-10-27 > "$EVIDENCE_DIR/auth-admin-dev-preview.json"

# Review every target and status before this separately authorized write.
LWC_APP_CONFIG_PATH="$AUTH_DEV_CONFIG" go run ./cmd/auth_management_migrate \
  -mode apply -manifest "$EVIDENCE_DIR/auth-admin-dev-preview.json" \
  -authorize-live-target dev

LWC_APP_CONFIG_PATH="$AUTH_DEV_CONFIG" go run ./cmd/auth_management_migrate \
  -mode readback -manifest "$EVIDENCE_DIR/auth-admin-dev-preview.json"

LWC_APP_CONFIG_PATH="$AUTH_DEV_CONFIG" go run ./cmd/auth_management_migrate \
  -mode central-cutover -manifest "$EVIDENCE_DIR/auth-admin-dev-preview.json" \
  -authorize-live-target dev \
  -ack-legacy-writers-retired auth-admin-dev-2026-10-27
```

The CLI's mutation guard compares `-authorize-live-target` with the loaded Auth `environment` (`dev`). The manifest records the configured `auth_session_environment` (`llm-wiki-cloud-dev`), which scopes the Firestore authority. Preview includes every current `users.role == "admin"` account, exact status and source update time, and both registered scopes. A target whose role, status, or update time changes after preview is skipped by apply and blocks central cutover until a fresh reviewed manifest and complete readback are available. Inactive targets may be skipped; do not assume that a legacy role will grant them authority after cutover.

Before preview, deploy both dual-read candidates and route account/admin mutations through Auth. Then stop the old independent role writer and let its in-flight updates finish. Review the entire preview and confirm that each intended operator is represented; do not bootstrap grants with a guessed user ID. After apply, read back both scope results for every row. Verify an intended operator can obtain fresh management capabilities, removing a scope invalidates its old capability, and ordinary account access remains available.

Initialization is idempotent and creates the shadow epoch plus stable scope-lock documents. Before any migration scope is applied, operators may redeploy compatible code and rerun initialization or preview without changing grants. After an apply, retry only with the same manifest and run ID; inspect readback before deciding whether another run is needed. Do not roll back to code that derives management rights from role after any explicit migration state exists. Central cutover is a one-way boundary: after it, recover by rolling forward with Auth's authority and current scope revisions, preserving revocations and migration markers.

The final BFF consumer requires current Auth scope capabilities and does not inspect Auth's private users or session store. Production, IAM changes, live grants, and live migration are outside this implementation.
