# Local development

The local app uses the configured Google Cloud project `llm-wiki-cloud`, its
`llm-wiki-cloud-local` GCS bucket, and the `llm-wiki-cloud-local` Firestore
database. Auth, BFF, and the native worker use Application Default Credentials
(ADC). The local pipeline never calls Cloud Run. Ask the project owner to
provision the bucket and database before starting; setup and IAM changes are
outside this guide.

## Prerequisites

- macOS with Go, Node.js/npm, Python 3.12 or newer, Make, and Google Cloud CLI
  installed. The pinned Synto runtime was verified with Python 3.14.6; make
  sure `python3` on `PATH` resolves to a supported interpreter before bootstrap.
- Network access to the public Pkl release and Python package hosts for the
  pinned Pkl CLI, Synto wheel, and its dependencies during bootstrap.
- Google Cloud ADC with access to the local bucket and database. To configure
  ADC interactively, run `gcloud auth application-default login` and select
  the `llm-wiki-cloud` project. Do not download a service-account key.
- The local target resources provisioned as listed above.

The Make commands reject a different project, bucket, or database. The local
scope and HTTP signing key live in this worktree's Git metadata directory, not
in the repository. Each worktree gets a stable, unique scope and key. They are
kept across restarts and never printed by `make local-config`.

## Start and stop

From the repository root, install dependencies once and start the native apps:

```sh
make bootstrap
make local-start
```

`make bootstrap` checks for Pkl CLI `0.32.1`, downloading the matching public
release binary into `.build/tools/pkl` when that pinned version is not already
on `PATH`. It also creates a worktree-private Python virtual environment under
the Git metadata directory and installs the same Synto `0.7.0` wheel pinned by
the worker Dockerfile, including its SHA-256 check. It verifies the Python
import/version and the `synto --version` command. The BFF's local worker child
inherits this venv at the front of `PATH`, so its `python3` adapter uses the
installed package; no global Python package is modified. A BFF start or
debugger target also ensures this runtime exists. Setup does not configure a
provider key or call a model.

The bootstrap uses the `python3` selected from your current `PATH` to create
that venv. On macOS, `/usr/bin/python3` may be an older system Python such as
3.9.6, which cannot install the pinned Synto dependency set (for example,
`No matching distribution found for mcp>=1.10`). Install or select Python 3.12
or newer, and put its `bin` directory before `/usr/bin` in `PATH`. For a
Homebrew Python 3.14 installation:

```sh
export PATH="$(brew --prefix python@3.14)/bin:$PATH"
command -v python3
python3 --version  # should report 3.12 or newer
make bootstrap
```

If an earlier bootstrap created the private venv with the older interpreter,
remove only that worktree's Synto venv after correcting `PATH`, then bootstrap
again:

```sh
rm -rf "$(git rev-parse --absolute-git-dir)/lwc361-local-cloud/python"
make bootstrap
```

`local-start` writes `apps/frontend/.env.local`, renders the local Pkl
configuration, builds the native worker, creates the admin/test account only
if it is absent in this worktree's Firestore scope, then starts Auth, BFF, and
Frontend. The Pkl source is `deploy/cac/local_fixture.pkl`; it generates
`.build/cac/local/local_fixture.yaml` for `cmd/local_fixture` and a separate
non-secret `local_demo.json` for Auth/BFF configuration. The admin/test email
is `admin-local@llm.wiki.dev`; its stable local-only password is in the Pkl
source. If that email already exists, its password, role, settings, and data
are left intact. Changing the Pkl password only affects a newly created
account; it does not reset an existing one.

The passwordless Demo button signs in as a separate, configured non-admin
account. Auth creates it only when its configured UID and email are unused,
with an empty default project; it does not seed example wiki content. If the
Demo identity conflicts with an older local account, startup keeps that account
unchanged and Demo login returns unavailable. Choose a free Demo UID/email in
the Pkl Demo section, regenerate local config, and restart the affected app.
You can also register another account through the app when email registration
is enabled.

```sh
make local-stop
```

The supervisor only stops processes it started for the current worktree. It
does not kill other processes that happen to use a configured port. Logs and
process state are in the worktree's Git metadata directory. `local-start`
waits for all selected loopback ports to accept connections; if a service
exits or fails readiness, it reports that service and its log path, then
cleans up the process groups it started.

Default ports are BFF `8080`, Auth `8081`, and Frontend `3000`. Override them
on start and stop consistently:

```sh
make local-start BFF_PORT=18080 AUTH_PORT=18081 FRONTEND_PORT=13000
make local-stop BFF_PORT=18080 AUTH_PORT=18081 FRONTEND_PORT=13000
```

The browser origins and frontend API URLs are derived from those ports. Local
HTTP accepts the loopback Host values `localhost` and `127.0.0.1`; use the
documented `http://localhost` URLs rather than a custom hostname. Auth
allows only the configured frontend origin for CORS and uses a host-only,
HttpOnly, SameSite refresh cookie suitable for loopback HTTP. Deployed Auth
continues to use its configured Host allowlist and Secure cookie policy.

## Component and debugger workflows

These targets share the same worktree scope, ADC, and configuration. Start the
supporting apps first, then run the target component in your IDE or terminal:

```sh
make -C apps/bff support-bff       # Auth + Frontend
make -C apps/bff bff-local          # run BFF in the current terminal

make -C apps/bff support-frontend  # Auth + BFF
make -C apps/bff frontend-local    # run Frontend in the current terminal

make -C apps/bff support-pipeline  # Auth + BFF + Frontend
make -C apps/bff pipeline-test     # unit tests; no paid provider request
make -C apps/bff local-synto-runtime-test # real Synto CLI boundary; no provider request
```

`auth-local` is also available for an Auth debugger session. Use `make
local-stop` to stop supervisor-managed support processes after debugging.

## Sign in and use the APIs

Sign in through the normal form with `admin-local@llm.wiki.dev` and the
local-only password in `deploy/cac/local_fixture.pkl`, or use the passwordless
Demo button. The two buttons use separate accounts: the admin/test account is
for local development, while Demo uses the restricted non-admin identity.
Login and refresh set the same local
refresh cookie policy; logout revokes the session. BFF APIs require the access
token returned by formal Auth and resolve the account from Firestore. They do
not accept `X-User-ID` as identity.

Use the response's `access_token` as a Bearer token for BFF requests. Keep the
token in your terminal session and do not paste it into logs or commit it.
The browser app handles refresh cookies automatically.

## Data scope and retention

All Firestore collections are rooted at `local_scopes/{scope}` and all GCS
objects at `local_scopes/{scope}/`. The same scope is used by Auth, BFF,
profile/export code, and the native worker. A separate worktree therefore
cannot see another worktree's local auth records, projects, execution state, or
objects. Ordinary local data persists across service restarts. Do not delete
the scope key or manually remove the matching cloud prefix while processes are
running. Test runs should use their own scope and remove only their own test
data after verification; there is no everyday-data reset command.

## Pipeline and adjacent features

The browser's pipeline action calls the local BFF, which starts the native
`olw_worker` process. Its pinned Synto adapter is installed in the worktree
runtime above. A real provider-backed pipeline still requires the existing
`LLM_API_KEY` or `DEEPSEEK_API_KEY` setting; bootstrap does not read, create, or
set those values. The worker reads and publishes through the configured
GCS bucket. Before the first pipeline run, render the selected local Pipeline
config from the checked-in SSOT. Repository Make targets use the pinned Pkl
CLI selected during bootstrap; direct Pkl and config-generator invocations can
use `pkl` on `PATH` or an explicit `PKL_BIN` path. Check it with
`pkl --version`:

The current Pipeline Job timeout is 7200 seconds. Render the local config
from the repository root with:

```sh
LWC_PIPELINE_RUN_TIMEOUT_SECONDS=7200 make config-local
```

If the Job timeout changes, use its verified value here; the renderer has no
default.

`make config-local` also needs the existing local provider key from
`LLM_API_KEY` or `DEEPSEEK_API_KEY`; keep the key only in your shell
environment. It writes `.build/cac/local/synto.toml` and a mode-`0600`
`private-bindings.json` in this worktree. Set
`CAC_OUTPUT_DIR=/path/to/cac-output` on both `make config-local` and the
`apps/bff` local start/debugger target if you use a custom output directory.
`local-start`, `support-frontend`, and `bff-local` pass those same-worktree
file paths to the native worker. If either file is missing or invalid, the
run reports a configuration error; it does not fetch DEV's GCS TOML, use an
old project `synto.toml`, upload the local config, or deploy a job. Deployed
workers without a local scope continue to read their run-start snapshot from
`pipeline-config/synto.toml` in GCS. Pipeline worker config rendering remains
explicit and does not run automatically during app startup.

The BFF cooldown projection is separate from Pipeline worker configuration.
`make config-local CAC_TARGET=bff` writes `.build/cac/local/bff.json` from the
current worktree without requiring a worker key or Job timeout. Managed BFF
starts (`local-start`, `support-frontend`, and `bff-local`) regenerate and
validate that projection before launch, then set `PIPELINE_COOLDOWN_SECONDS`
only in the BFF child process; Auth and Frontend keep their existing
environment.

Each local generation manifest carries the execution ID in the
same conditional write that commits it as current. After that write is
acknowledged, the worker adds a create-only receipt with the execution and
manifest generations; an acknowledged write is not a separate manifest
readback. BFF reads the current manifest and receipt when recording the result,
and can recover a missing receipt only when the committed manifest names that
execution. A newer current manifest from another run yields an unknown result
for the earlier execution. This path does not create, deploy, or call a Cloud
Run job. Normal
pipeline work still needs the existing provider configuration and may incur
provider charges; do not use it for a no-cost smoke check. `pipeline-test`
and the repository's loopback smoke tests do not call a paid LLM. The smoke
suite also has a no-cost subprocess contract test when both Firestore and
Storage emulators are running on loopback:

```sh
cd apps/bff
FIRESTORE_EMULATOR_HOST=127.0.0.1:8085 \
STORAGE_EMULATOR_HOST=http://127.0.0.1:4443 \
go test ./cmd/bff -run '^TestLocalPipelineHTTPTriggerRunsWorkerAndReportsSuccessAndFailure$' -count=1 -v
```

That test builds a fixture-tagged worker which replaces only Synto execution;
the BFF trigger/status routes, subprocess, scoped Firestore state, GCS
publisher, manifest commit, and readback remain real. It covers a successful
run, duplicate trigger rejection, child failure, and spawn failure. It seeds a
previously successful source receipt and verifies that the exact historical
source bytes and compiled source page carry forward to the next generation. It
points the Cloud Run job URL at a loopback interceptor and asserts that native
trigger/status calls make zero requests to it. It
uses a unique test-run scope, reads back that the scope is empty after cleanup,
and proves a sentinel in another scope remains. The test refuses non-loopback
emulator endpoints and skips when either emulator endpoint is unset. It does
not use a real provider key.

The local BFF does not run Export jobs: the Export create endpoint returns
`503 export_unavailable` before admission and never calls Cloud Run. Profile
read and edit APIs may be available, but Make does not start a Profile
scheduler dispatcher; queued background Profile work stays pending for the
supported deployment dispatcher. Local setup does not configure a scheduler
audience or service account. Recompile-all remains unavailable with
`403 byok_required`; the local app does not provide a BYOK provider route.

The deployed passwordless Demo endpoint remains backed by the configured,
existing formal Demo account and session authority. Local Auth does not mount
that endpoint; use the normal password account above for local sign-in.

Google OAuth callback development still depends on the separately configured
OAuth redirect URI and HTTPS callback handling. The local HTTP path documents
password registration/login/refresh/logout; it does not change production
OAuth or deployed cookie/Host policy.

## Common errors

- **Wrong local target:** remove conflicting `GCP_PROJECT`,
  `GOOGLE_CLOUD_PROJECT`, `BUCKET`, or `FIRESTORE_DATABASE_ID` overrides and
  use the fixed local target. The commands intentionally reject mismatches.
- **ADC or permission error:** authenticate ADC and ask the resource owner to
  verify access to the already-provisioned local bucket/database. This guide
  does not create resources or change IAM.
- **Missing local resources:** ask the project owner to provision the exact
  local bucket/database. The app does not fall back to filesystem storage or
  another database.
- **Port is in use:** choose a different port override for all start/debugger
  and stop commands. Stop only processes managed by this worktree.
- **No worker binary:** `make local-start` builds it. For a debugger workflow,
  run `make -C apps/bff local-worker-build` before starting BFF.
- **Pipeline provider config is absent:** use `pipeline-test` or the loopback
  smoke check. For a real local pipeline, render the local files with
  `make config-local`, set `LWC_PIPELINE_RUN_TIMEOUT_SECONDS` to the verified
  Pipeline Job timeout, and provide the existing local provider key. A real
  run may incur charges.
- **Pinned Synto install reports no matching `mcp>=1.10` distribution:** check
  `command -v python3` and `python3 --version`. macOS may be selecting its
  Python 3.9 system interpreter; select Python 3.12 or newer before creating
  the worktree venv, then remove only that venv and rerun `make bootstrap` as
  shown above. This error can come from the selected Python version even when
  the pinned wheel URL is reachable.
