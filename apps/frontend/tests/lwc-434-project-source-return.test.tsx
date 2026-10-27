import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';

const mocks = vi.hoisted(() => ({
  getProjectLLMBinding: vi.fn(),
  listLLMProfiles: vi.fn(),
  saveProjectLLMBinding: vi.fn(),
}));

vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api');
  return {
    ...actual,
    getProjectLLMBinding: mocks.getProjectLLMBinding,
    listLLMProfiles: mocks.listLLMProfiles,
    saveProjectLLMBinding: mocks.saveProjectLLMBinding,
  };
});

import { ProjectLLMSourcePanel } from '@/components/ProjectLLMSourcePanel';
import type { LLMProfile } from '@/lib/api';

const createdProfile: LLMProfile = {
  id: 'created-profile',
  name: 'New BYOK profile',
  provider: 'custom',
  protocol: 'openai_chat_completions',
  endpoint: 'https://fixture.example/v1',
  model: 'fixture-model',
  revision: 1,
  secret_configured: true,
};

beforeEach(() => {
  localStorage.setItem('locale', 'en');
  mocks.getProjectLLMBinding.mockReset().mockResolvedValue({ source: 'shared', revision: 1 });
  mocks.listLLMProfiles.mockReset().mockResolvedValue([]);
  mocks.saveProjectLLMBinding.mockReset().mockResolvedValue({ source: 'personal', profile_id: createdProfile.id, revision: 2 });
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe('LWC-434 Project BYOK return flow', () => {
  it('selects the returned profile in the unsaved Project draft, then saves only on explicit action', async () => {
    const onAddProfile = vi.fn();
    const view = render(
      <ProjectLLMSourcePanel projectId="project-a" ownerId="owner-a" sessionEpoch={1} onAddProfile={onAddProfile} />,
    );
    await screen.findByRole('combobox', { name: 'Project LLM source' });

    fireEvent.change(screen.getByRole('combobox', { name: 'Project LLM source' }), { target: { value: 'personal' } });
    fireEvent.click(screen.getByRole('button', { name: 'Go to Personal settings' }));
    expect(onAddProfile).toHaveBeenCalledOnce();

    view.rerender(
      <ProjectLLMSourcePanel
        projectId="project-a"
        ownerId="owner-a"
        sessionEpoch={1}
        onAddProfile={onAddProfile}
        newlyCreatedProfile={createdProfile}
      />,
    );

    await waitFor(() => {
      expect((screen.getByRole('combobox', { name: 'Your profile' }) as HTMLSelectElement).value).toBe(createdProfile.id);
    });
    expect(mocks.saveProjectLLMBinding).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: 'Save Project source' }));
    await waitFor(() => expect(mocks.saveProjectLLMBinding).toHaveBeenCalledWith('project-a', {
      source: 'personal', profile_id: createdProfile.id, revision: 1,
    }));
  });
});
