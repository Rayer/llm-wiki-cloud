import assert from 'node:assert/strict';
import test from 'node:test';

import { ApiError, configureApiAuth, getAdminSyncBindingRevocation } from '../src/lib/api.ts';

const validReceipt = {
  schema_version: 1,
  operation_id: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',
  authority: 'lwc',
  environment: 'demo',
  actor_id: 'admin-a',
  subject: 'owner-a',
  target_kind: 'sync_binding',
  target_id: 'binding-a',
  client_id: null,
  project_id: 'project-a',
  reason: 'security incident',
  result: 'revoked',
  committed_at: '2026-11-03T00:01:00Z',
  management_authorized_at: '2026-11-03T00:00:59Z',
  previous_cli_epoch: null,
  current_cli_epoch: null,
  affected_count: 1,
  count_complete: true,
};

function installAuth() {
  configureApiAuth({ getAccessToken: () => 'fixture-token', refreshAccessToken: async () => null, onUnauthorized: () => undefined });
  globalThis.window = { localStorage: { getItem: () => null } };
}

test('sync binding revocation receipt decoder rejects incomplete or contradictory records', async () => {
  installAuth();
  const originalFetch = globalThis.fetch;
  try {
    for (const [field, value] of [
      ['environment', null],
      ['subject', null],
      ['result', 'unknown'],
      ['target_kind', 'cli_session'],
      ['affected_count', 0],
    ]) {
      globalThis.fetch = async () => Response.json({ ...validReceipt, [field]: value });
      await assert.rejects(
        () => getAdminSyncBindingRevocation(validReceipt.operation_id),
        (error) => error instanceof ApiError && error.status === 503,
        `${field} must be validated at the API boundary`,
      );
    }

    globalThis.fetch = async () => Response.json(validReceipt);
    assert.deepEqual(await getAdminSyncBindingRevocation(validReceipt.operation_id), validReceipt);
  } finally {
    globalThis.fetch = originalFetch;
  }
});
