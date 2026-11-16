import { beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({ apiFetch: vi.fn() }));

vi.mock('@/lib/api', () => ({ apiFetch: mocks.apiFetch }));

import { getNudges, reconcileNudges, resolveNudgeTarget, setNudgeAttention } from '@/lib/nudges';

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

  it('sends unread as an explicit attention action', async () => {
    mocks.apiFetch.mockResolvedValueOnce({ ok: true, status: 204 } as Response);
    await setNudgeAttention('content-purpose-gap', 'attention-exact', 'unread');
    expect(mocks.apiFetch).toHaveBeenCalledWith('/api/v1/nudges/content-purpose-gap/attention', expect.objectContaining({
      method: 'PATCH',
      body: JSON.stringify({ attention_id: 'attention-exact', action: 'unread' }),
    }));
  });

  it('resolves a same-Project target from the nested HTTP observation shape', async () => {
    mocks.apiFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        schema_version: 1, partial: false, failures: [], notices: [], items: [{
          category: 'pipeline-failure', severity: 'attention', observations: [{
            source_key: 'pipeline-fail:project-a', source_revision: 1, active: true, observation: {
              project_id: 'project-a', project_name: 'Named Project', title: 'Pipeline did not publish',
              body: 'The run ended before publication.', severity: 'attention',
              coverage: { total_concepts: 0, selected_concepts: 0, sampled: false, title_truncated_concepts: 0, body_truncated_concepts: 0 },
              actions: [{ id: 'open-run', kind: 'link', label: 'Open run', target: { kind: 'pipeline-run', project_id: 'project-a', execution_id: 'execution-1' } }],
            },
          }],
        }],
      }),
    });

    const view = await getNudges();
    const target = view.items[0]?.observations[0]?.observation?.actions[0]?.target;

    expect(view.items[0]?.observations[0]?.observation?.project_name).toBe('Named Project');
    expect(view.items[0]?.observations[0]?.observation?.coverage).toEqual({
      total_concepts: 0, selected_concepts: 0, sampled: false, title_truncated_concepts: 0, body_truncated_concepts: 0,
    });
    expect(target && resolveNudgeTarget(target)).toBe('/status?project_id=project-a&execution_id=execution-1');
  });
});
