import { beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({ apiFetch: vi.fn() }));

vi.mock('@/lib/api', () => ({ apiFetch: mocks.apiFetch }));

import { getNudges, reconcileNudges } from '@/lib/nudges';

describe('Nudge API read and reconcile contracts', () => {
  beforeEach(() => {
    mocks.apiFetch.mockReset().mockResolvedValue({
      ok: true,
      json: async () => ({ schema_version: 1, partial: true, failures: [], notices: [], items: [] }),
    });
  });

  it('reads only the persisted account view with GET', async () => {
    await getNudges();

    expect(mocks.apiFetch).toHaveBeenCalledWith('/api/v1/nudges', { method: 'GET', requireProject: false });
  });

  it('uses an explicit reconcile POST to surface current adapter status', async () => {
    const view = await reconcileNudges();

    expect(mocks.apiFetch).toHaveBeenCalledWith('/api/v1/nudges/reconcile', { method: 'POST', requireProject: false });
    expect(view.partial).toBe(true);
  });
});
