# LWC-346 DEV UAT guide

## Before testing

Have the DEV operator read back the deployed Auth, BFF, and frontend origins and the BFF API URL, then set these placeholders to those observed values. Do not copy endpoint values from a source config as proof of the live deployment.

```sh
export DEV_AUTH_ORIGIN='<operator-read-back-auth-origin>'
export DEV_WIKI_ORIGIN='<operator-read-back-wiki-origin>'
export DEV_BFF_ORIGIN='<operator-read-back-bff-origin>'
```

Use a DEV test account that owns a DEV Project with exportable data. The CLI supports only the Auth/control-plane origin; it does not accept a separate data-sync service origin.

The export-job CD component is DEV-only and is disabled in config until an operator confirms the Cloud Run Job name, execution identity, signing identity, and existing runtime environment. It will only update the immutable job image; it will not create the job, alter job environment variables, grant IAM, or update bucket lifecycle. Before enabling it, provision and read back the Cloud Run Job with `GCP_PROJECT`, `BUCKET`, `FIRESTORE_DATABASE_ID`, and `EXPORT_SIGNING_SERVICE_ACCOUNT`, and confirm its execution/signing permissions through the authorized operator process. The same reviewed setting wires `EXPORT_JOB_URL` and `EXPORT_SIGNING_SERVICE_ACCOUNT` into the BFF revision. Exact identities and provider grants are currently unknown and must come from that readback.

## Build and CLI login

From the repository root, build the CLI with the existing target:

```sh
make -C apps/bff build-sync
./apps/bff/lwc-sync auth login --host "$DEV_AUTH_ORIGIN" --no-browser
```

Open the printed verification URL in a browser signed into the DEV account, enter the printed code, and explicitly approve the CLI. A pairing code expires after 10 minutes; the CLI polls every 3 seconds. Then verify and select the owned Project:

```sh
./apps/bff/lwc-sync auth status
./apps/bff/lwc-sync projects
```

Expected: status reports the signed-in account, and the Projects list contains only Projects owned by that account. Pairing approval alone does not grant access until the CLI redeems the code.

## Export flow

1. Sign into the DEV wiki as the test account, open an owned Project, and go to its Pipeline page.
2. In the **Export** section, choose one scope: `raw`, `raw-full`, or `raw-full-metadata`; review the secret/config exclusions and confirm **Start export**.
3. Expected: the UI shows a preparing state, then a ready archive with scope, size, snapshot time, and expiry. Download it and confirm it is a ZIP containing the selected export data. Do not expect compiled artifacts or a restore operation.
4. Refresh the page and confirm the ready archive remains listed and downloadable until its expiry.
5. Start another export immediately after a successful completion. Expected: the UI displays the next allowed time and blocks another request during the 24-hour cooldown. A failed export does not start the successful-export cooldown; retry only when the UI says the Project is eligible.
6. At or after the displayed expiry, expected: the app refuses downloads and displays the archive as unavailable/expired.

The API's download boundary is 72 hours after archive completion. GCS lifecycle deletion is asynchronous, and bucket soft-delete, versioning, retention, and holds may keep bytes recoverable after that boundary; the UI expiry is not a physical-erasure guarantee. A running export has a 22-hour hard timeout and a 23-hour job deadline.

## CLI binding and revocation

Use a disposable test vault containing no valuable data. Replace the placeholders with the local vault path and a Project ID from the preceding command:

```sh
export TEST_VAULT='<path-to-disposable-wiki-vault>'
export DEV_PROJECT_ID='<owned-dev-project-id>'
./apps/bff/lwc-sync bind --vault "$TEST_VAULT" --project-id "$DEV_PROJECT_ID"
./apps/bff/lwc-sync binding list
```

Expected: the list shows one active binding for the Project. Binding authorizes a wiki/Project relationship; it does not upload, download, or synchronize wiki data.

In the DEV wiki, open **Account settings**. Under **Sync bindings**, confirm the same Project and binding ID appear. Revoke it and confirm its status changes to **Revoked**. Run `binding list` again and confirm the server reports it revoked. The CLI session should still work:

```sh
./apps/bff/lwc-sync auth status
```

The revoked binding must no longer authorize binding-protected requests. Reauthorization is explicit; for an end-to-end check, use Account settings' **Reauthorize binding** control, then confirm the binding is active and the vault is synchronized to the replacement binding:

```sh
./apps/bff/lwc-sync binding reauthorize --vault "$TEST_VAULT" --project-id "$DEV_PROJECT_ID"
./apps/bff/lwc-sync binding list
```

No wiki data is transferred by bind, reauthorize, or revoke.

## CLI session revocation

After login, keep one terminal available with a fresh CLI session. The short-lived access token lasts 15 minutes and refresh credentials rotate with a seven-day lifetime; the server CLI session has no idle expiration and remains active until logout, explicit revoke, or account state changes. In the DEV wiki, open **Account settings** and under **CLI sessions**, revoke that session. Expected: the session is shown as revoked, and subsequent CLI authenticated requests fail; `auth status` should no longer report a signed-in session. Sign in again to continue other checks.

```sh
./apps/bff/lwc-sync auth status
./apps/bff/lwc-sync auth login --host "$DEV_AUTH_ORIGIN" --no-browser
```

Approve the new pairing in the browser before the login command completes. At the end of testing, sign out locally:

```sh
./apps/bff/lwc-sync auth logout
```

Logout clears the local CLI credentials and revokes the server session. These checks require a successfully deployed DEV build; source tests and image builds do not establish live DEV behavior.
