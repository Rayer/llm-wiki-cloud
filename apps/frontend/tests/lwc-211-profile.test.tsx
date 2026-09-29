import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { ProfileBootstrapGuidance, ProfileRequirement, ProfileState } from '@/lib/api';

if (!(React as { act?: (callback: () => unknown) => Promise<unknown> | unknown }).act) {
  Object.defineProperty(React, 'act', {
    configurable: true,
    value: (callback: () => unknown) => Promise.resolve(callback()),
  });
}

const { act, cleanup, fireEvent, render, screen, waitFor } = await import('@testing-library/react');

const mocks = vi.hoisted(() => ({
  getProfile: vi.fn(),
  getProfileBootstrapGuidance: vi.fn(),
  getProfileGuidanceArtifact: vi.fn(),
  confirmProfileBootstrapGuidance: vi.fn(),
  saveProfile: vi.fn(),
  confirmProfileCandidate: vi.fn(),
  retryProfileCandidate: vi.fn(),
  retryProfileDerivation: vi.fn(),
  getProfileJob: vi.fn(),
  getRecompileAllCapability: vi.fn(),
  recompileAll: vi.fn(),
}));

vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api');
  return {
    ...actual,
    getProfile: mocks.getProfile,
    getProfileBootstrapGuidance: mocks.getProfileBootstrapGuidance,
    getProfileGuidanceArtifact: mocks.getProfileGuidanceArtifact,
    confirmProfileBootstrapGuidance: mocks.confirmProfileBootstrapGuidance,
    saveProfile: mocks.saveProfile,
    confirmProfileCandidate: mocks.confirmProfileCandidate,
    retryProfileCandidate: mocks.retryProfileCandidate,
    retryProfileDerivation: mocks.retryProfileDerivation,
    getProfileJob: mocks.getProfileJob,
    getRecompileAllCapability: mocks.getRecompileAllCapability,
    recompileAll: mocks.recompileAll,
  };
});

import { ProjectProfilePanel } from '@/components/ProjectProfilePanel';
import { ProfileRequirementsEditor } from '@/components/ProfileRequirementsEditor';

function emptyProfile(projectId = 'project-a', revision = 0): ProfileState {
  return {
    project_id: projectId,
    revision,
    requirements: [],
    derivation_status: null,
    scheduled_for: null,
    derivation_error_code: null,
    candidate: null,
    confirmed_candidate_id: null,
    active: null,
    job: null,
  };
}

function profileWithCandidate(source: 'manual' | 'compile_auto' = 'manual'): ProfileState {
  return {
    project_id: 'project-a',
    revision: 6,
    requirements: [{ id: 'req-1', text: 'invoice source' }],
    derivation_status: 'ready',
    scheduled_for: null,
    derivation_error_code: null,
    candidate: {
      candidate_id: 'candidate-2',
      source,
      base_revision: 6,
      requirements_digest: 'digest-6',
      content_generation: 'generation-9',
      dictionary: { revision: 'dict-2', input_digest: 'digest-6', model_version: 'm1', prompt_version: 'p1', schema_version: 's1' },
      guidance: { revision: 'guide-2', input_digest: 'digest-6', model_version: 'm1', prompt_version: 'p1', schema_version: 's1' },
      preview: {
        dictionary_diff: 'Added invoice tag',
        guidance_diff: 'Prefer dated invoices',
        requirements: [{ id: 'req-1', disposition: 'both', explanation: 'Applied to tagging and writing.' }],
      },
    },
    confirmed_candidate_id: null,
    active: {
      candidate_id: 'candidate-1',
      content_generation: 'generation-8',
      dictionary_revision: 'dict-1',
      tag_set_revision: 'tags-1',
      query_rule_revision: 'query-1',
      guidance_revision: 'guide-1',
    },
    job: {
      job_id: 'job-2',
      candidate_id: 'candidate-2',
      content_generation: 'generation-9',
      status: 'incomplete',
      missing_count: 2,
      error_code: 'tag_timeout',
    },
  };
}

function bootstrapGuidance(
  status: ProfileBootstrapGuidance['status'] = 'preview_ready',
  profileRevision = 1,
  inputDigest = 'input-digest-1',
): ProfileBootstrapGuidance {
  return {
    revision: 'sha256-bootstrap-1',
    input_digest: inputDigest,
    profile_revision: profileRevision,
    status,
    model_version: 'model-1',
    prompt_version: 'prompt-1',
    schema_version: 'profile.bootstrap-guidance.v1',
    confirmed_at: status === 'confirmed' ? '2026-09-25T03:00:00Z' : null,
    preview: {
      guidance_diff: 'Prefer concrete, dated explanations.',
      requirements: [
        { id: 'req-1', disposition: 'compile_guidance', explanation: 'Keep the writing concise and source-linked.' },
        { id: 'req-2', disposition: 'limitation', explanation: 'This request cannot be guaranteed during compilation.' },
      ],
    },
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

beforeEach(() => {
  localStorage.setItem('locale', 'en');
  mocks.getProfile.mockResolvedValue(emptyProfile());
  mocks.getProfileBootstrapGuidance.mockResolvedValue({ bootstrap_guidance: null });
  mocks.getProfileGuidanceArtifact.mockImplementation(async (_projectId: string, revision: string) => ({
    guidance_artifact: {
      revision,
      input_digest: 'b'.repeat(64),
      model_version: revision === 'guide-1' ? 'active-model-v1' : 'immutable-model-v1',
      prompt_version: revision === 'guide-1' ? 'active-prompt-v1' : 'immutable-prompt-v1',
      schema_version: revision === 'sha256-bootstrap-1' ? 'profile.bootstrap-guidance.v1' : 'profile.guidance.v1',
      compile_guidance: revision === 'guide-1' ? 'Active immutable instructions.' : 'Exact immutable instructions.',
    },
  }));
  mocks.confirmProfileBootstrapGuidance.mockImplementation(async (_projectId: string, _revision: string, profileRevision: number, inputDigest: string) => ({
    bootstrap_guidance: { ...bootstrapGuidance('confirmed', profileRevision, inputDigest) },
  }));
  mocks.saveProfile.mockImplementation(async (projectId: string, revision: number, requirements: unknown[]) => ({
    ...emptyProfile(projectId, revision + 1),
    requirements,
    derivation_status: 'pending',
    scheduled_for: '2026-09-25T03:00:00Z',
  }));
  mocks.confirmProfileCandidate.mockImplementation(async () => profileWithCandidate());
  mocks.retryProfileCandidate.mockImplementation(async () => profileWithCandidate());
  mocks.retryProfileDerivation.mockResolvedValue(emptyProfile('project-a', 4));
  mocks.getProfileJob.mockResolvedValue(profileWithCandidate().job);
  mocks.getRecompileAllCapability.mockResolvedValue({ allowed: false, denial_code: 'byok_required' });
  mocks.recompileAll.mockResolvedValue({ status: 'accepted' });
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.clearAllMocks();
});

describe('LWC-211 Project Profile', () => {
  it('keeps the empty requirement editor mounted and focused after the first character', async () => {
    function RequirementsHarness() {
      const [requirements, setRequirements] = React.useState<ProfileRequirement[]>([]);
      return (
        <>
          <ProfileRequirementsEditor requirements={requirements} onChange={setRequirements} />
          <output aria-label="requirements state">{JSON.stringify(requirements)}</output>
        </>
      );
    }

    render(<RequirementsHarness />);
    const firstInput = await screen.findByRole('textbox', { name: 'Requirement 1' });
    firstInput.focus();
    fireEvent.change(firstInput, { target: { value: 'x' } });

    const updatedInput = screen.getByRole('textbox', { name: 'Requirement 1' });
    expect(updatedInput).toBe(firstInput);
    expect(document.activeElement).toBe(updatedInput);
  });

  it('preserves sibling requirements when editing or clearing the first requirement', () => {
    function RequirementsHarness() {
      const [requirements, setRequirements] = React.useState<ProfileRequirement[]>([]);
      return (
        <>
          <ProfileRequirementsEditor requirements={requirements} onChange={setRequirements} />
          <output aria-label="requirements state">{JSON.stringify(requirements)}</output>
        </>
      );
    }

    render(<RequirementsHarness />);
    fireEvent.change(screen.getByRole('textbox', { name: 'Requirement 1' }), { target: { value: 'first' } });
    fireEvent.click(screen.getByRole('button', { name: 'Add requirement' }));
    fireEvent.change(screen.getByRole('textbox', { name: 'Requirement 2' }), { target: { value: 'second' } });

    fireEvent.change(screen.getByRole('textbox', { name: 'Requirement 1' }), { target: { value: 'updated first' } });
    const afterEdit = JSON.parse(screen.getByLabelText('requirements state').textContent ?? '[]') as ProfileRequirement[];
    expect(afterEdit).toHaveLength(2);
    expect(afterEdit.map(({ text }) => text)).toEqual(['updated first', 'second']);

    fireEvent.change(screen.getByRole('textbox', { name: 'Requirement 1' }), { target: { value: '' } });
    const afterClear = JSON.parse(screen.getByLabelText('requirements state').textContent ?? '[]') as ProfileRequirement[];
    expect(afterClear).toHaveLength(2);
    expect(afterClear.map(({ text }) => text)).toEqual(['', 'second']);
  });

  it('saves ordered requirements with the server revision and reports the three-minute pending schedule', async () => {
    render(<ProjectProfilePanel projectId="project-a" />);

    const input = await screen.findByRole('textbox', { name: 'Requirement 1' });
    expect(mocks.getProfileBootstrapGuidance).not.toHaveBeenCalled();
    expect(screen.queryByRole('heading', { name: 'First-compile guidance' })).toBeNull();
    fireEvent.change(input, { target: { value: '  invoice source  ' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save requirements' }));

    await waitFor(() => expect(mocks.saveProfile).toHaveBeenCalledWith(
      'project-a',
      0,
      [expect.objectContaining({ text: '  invoice source  ' })],
    ));
    expect(await screen.findByText(/scheduled by the server/i)).toBeDefined();
    expect(screen.getByText(/2026-09-25T03:00:00Z/)).toBeDefined();
    expect(screen.queryByText('Added invoice tag')).toBeNull();
    expect(screen.queryByRole('button', { name: /Recompile all/i })).toBeNull();
    expect(mocks.getRecompileAllCapability).not.toHaveBeenCalled();
  });

  it('shows first-compile guidance effects, explicitly confirms it, and keeps it separate from an active Profile', async () => {
    const profile = {
      ...emptyProfile('project-a', 1),
      requirements: [{ id: 'req-1', text: 'write concise summaries' }],
      derivation_status: 'ready' as const,
    };
    mocks.getProfile.mockResolvedValue(profile);
    mocks.getProfileBootstrapGuidance.mockResolvedValue({ bootstrap_guidance: bootstrapGuidance() });
    mocks.confirmProfileBootstrapGuidance.mockResolvedValue({ bootstrap_guidance: bootstrapGuidance('confirmed') });

    render(<ProjectProfilePanel projectId="project-a" />);

    expect(await screen.findByText('Prefer concrete, dated explanations.')).toBeDefined();
    expect(await screen.findByText('Exact immutable instructions.')).toBeDefined();
    expect(screen.getByText((_text, element) => element?.tagName === 'P' && element.textContent?.includes(
      'Model immutable-model-v1 · Prompt immutable-prompt-v1 · Schema profile.bootstrap-guidance.v1',
    ) === true)).toBeDefined();
    expect(screen.getByText(/req-1: Compile guidance/)).toBeDefined();
    expect(screen.getByText(/req-2: Limitation/)).toBeDefined();
    expect(screen.getByText(/No generation-bound Profile is active yet/)).toBeDefined();
    fireEvent.click(screen.getByRole('button', { name: 'Confirm for first compile' }));

    await waitFor(() => expect(mocks.confirmProfileBootstrapGuidance).toHaveBeenCalledWith(
      'project-a',
      'sha256-bootstrap-1',
      1,
      'input-digest-1',
    ));
    expect(await screen.findByText(/Confirmed for first compile/)).toBeDefined();
    expect(screen.getByText(/does not start tagging/)).toBeDefined();
    expect(screen.queryByText(/Active profile:/)).toBeNull();
    expect(screen.queryByText(/Tagging work:/)).toBeNull();
  });

  it('shows the candidate guidance artifact text and its immutable version metadata', async () => {
    mocks.getProfile.mockResolvedValue(profileWithCandidate());

    render(<ProjectProfilePanel projectId="project-a" />);

    expect(await screen.findByText('Exact immutable instructions.')).toBeDefined();
    expect(await screen.findByText('Active immutable instructions.')).toBeDefined();
    expect(screen.getByRole('heading', { name: 'Active writing guidance' })).toBeDefined();
    expect(screen.getByRole('heading', { name: 'Candidate writing guidance' })).toBeDefined();
    expect(screen.getByText('guide-2', { selector: 'code' })).toBeDefined();
    expect(screen.getByText((_text, element) => element?.tagName === 'P' && element.textContent?.includes(
      'Model immutable-model-v1 · Prompt immutable-prompt-v1 · Schema profile.guidance.v1',
    ) === true)).toBeDefined();
    expect(screen.getByText(/req-1: Compile guidance and dictionary or query/)).toBeDefined();
    expect(screen.getByText('Applied to tagging and writing.')).toBeDefined();
    expect(screen.getAllByText(/1,500-character adapter budget/).length).toBe(2);
  });

  it('shows retained active guidance after saving requirements clears the prior preview', async () => {
    mocks.getProfile.mockResolvedValue({
      ...profileWithCandidate(),
      revision: 7,
      requirements: [{ id: 'req-2', text: 'new writing' }],
      derivation_status: 'pending',
      scheduled_for: '2026-09-25T03:05:00Z',
      candidate: null,
      confirmed_candidate_id: null,
      job: null,
    });

    render(<ProjectProfilePanel projectId="project-a" />);

    expect(await screen.findByText('Active immutable instructions.')).toBeDefined();
    expect(screen.getByRole('heading', { name: 'Active writing guidance' })).toBeDefined();
    expect(screen.queryByRole('heading', { name: 'Candidate writing guidance' })).toBeNull();
    expect(screen.getByText(/1,500-character adapter budget/)).toBeDefined();
  });

  it('keeps confirmation after a polling GET resolves late with the same-revision preview', async () => {
    const pending = {
      ...emptyProfile('project-a', 1),
      requirements: [{ id: 'req-1', text: 'write concise summaries' }],
      derivation_status: 'pending' as const,
    };
    const delayedRead = deferred<{ bootstrap_guidance: ProfileBootstrapGuidance }>();
    const delayedConfirmation = deferred<{ bootstrap_guidance: ProfileBootstrapGuidance }>();
    mocks.getProfile.mockResolvedValue(pending);
    mocks.getProfileBootstrapGuidance
      .mockResolvedValueOnce({ bootstrap_guidance: bootstrapGuidance() })
      .mockReturnValueOnce(delayedRead.promise);
    mocks.confirmProfileBootstrapGuidance.mockReturnValue(delayedConfirmation.promise);
    vi.useFakeTimers();

    render(<ProjectProfilePanel projectId="project-a" />);
    await act(async () => { await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); });
    expect(screen.getByRole('region', { name: 'First-compile guidance' }).getAttribute('aria-busy')).toBe('false');
    fireEvent.click(screen.getByRole('button', { name: 'Confirm for first compile' }));
    await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
    expect(mocks.getProfileBootstrapGuidance).toHaveBeenCalledTimes(2);
    expect(screen.getByRole('region', { name: 'First-compile guidance' }).getAttribute('aria-busy')).toBe('true');

    await act(async () => {
      delayedConfirmation.resolve({ bootstrap_guidance: bootstrapGuidance('confirmed') });
      await delayedConfirmation.promise;
      await Promise.resolve();
    });
    expect(screen.getByText(/Confirmed for first compile/)).toBeDefined();
    expect(screen.getByRole('region', { name: 'First-compile guidance' }).getAttribute('aria-busy')).toBe('false');

    await act(async () => {
      delayedRead.resolve({ bootstrap_guidance: bootstrapGuidance('preview_ready') });
      await delayedRead.promise;
      await Promise.resolve();
    });
    expect(screen.getByText(/Confirmed for first compile/)).toBeDefined();
    expect(screen.queryByRole('button', { name: 'Confirm for first compile' })).toBeNull();
  });

  it('does not confirm a bootstrap preview for a stale Profile revision', async () => {
    const profile = {
      ...emptyProfile('project-a', 2),
      requirements: [{ id: 'req-1', text: 'write concise summaries' }],
      derivation_status: 'ready' as const,
    };
    mocks.getProfile.mockResolvedValue(profile);
    mocks.getProfileBootstrapGuidance.mockResolvedValue({ bootstrap_guidance: bootstrapGuidance('preview_ready', 1) });

    render(<ProjectProfilePanel projectId="project-a" />);

    expect(await screen.findByText(/first compile is waiting for confirmed bootstrap guidance/i)).toBeDefined();
    expect(screen.queryByRole('button', { name: 'Confirm for first compile' })).toBeNull();
    expect(mocks.confirmProfileBootstrapGuidance).not.toHaveBeenCalled();
  });

  it('preserves the requirement draft and refreshes readback after a stale guidance digest conflict', async () => {
    const profile = {
      ...emptyProfile('project-a', 1),
      requirements: [{ id: 'req-1', text: 'my saved requirement' }],
      derivation_status: 'ready' as const,
    };
    const latest = {
      ...emptyProfile('project-a', 2),
      requirements: [{ id: 'req-1', text: 'server updated requirement' }],
      derivation_status: 'pending' as const,
    };
    mocks.getProfile.mockResolvedValueOnce(profile).mockResolvedValueOnce(latest);
    mocks.getProfileBootstrapGuidance
      .mockResolvedValueOnce({ bootstrap_guidance: bootstrapGuidance('preview_ready', 1, 'stale-digest') })
      .mockResolvedValueOnce({ bootstrap_guidance: null });
    mocks.confirmProfileBootstrapGuidance.mockRejectedValue(Object.assign(new Error('input digest conflict'), { status: 409 }));

    render(<ProjectProfilePanel projectId="project-a" />);
    fireEvent.click(await screen.findByRole('button', { name: 'Confirm for first compile' }));

    expect(await screen.findByText('Bootstrap guidance changed elsewhere. Latest Profile state was reloaded; your requirements draft is kept.')).toBeDefined();
    expect((screen.getByRole('textbox', { name: 'Requirement 1' }) as HTMLInputElement).value).toBe('my saved requirement');
    expect(mocks.getProfileBootstrapGuidance).toHaveBeenCalledTimes(2);
    expect(screen.queryByText(/Confirmed for first compile/)).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Save requirements' }));
    await waitFor(() => expect(mocks.saveProfile).toHaveBeenCalledWith(
      'project-a',
      2,
      [{ id: 'req-1', text: 'my saved requirement' }],
    ));
  });

  it('does not claim latest Profile state was reloaded when a 409 readback fails', async () => {
    const profile = {
      ...emptyProfile('project-a', 1),
      requirements: [{ id: 'req-1', text: 'keep my draft' }],
      derivation_status: 'ready' as const,
    };
    mocks.getProfile.mockResolvedValueOnce(profile).mockRejectedValueOnce(new Error('readback unavailable'));
    mocks.getProfileBootstrapGuidance.mockResolvedValue({ bootstrap_guidance: bootstrapGuidance() });
    mocks.confirmProfileBootstrapGuidance.mockRejectedValue(Object.assign(new Error('digest conflict'), { status: 409 }));

    render(<ProjectProfilePanel projectId="project-a" />);
    fireEvent.click(await screen.findByRole('button', { name: 'Confirm for first compile' }));

    expect(await screen.findByText('Bootstrap guidance changed elsewhere. The latest Profile state could not be confirmed; your requirements draft is kept.')).toBeDefined();
    expect((screen.getByRole('textbox', { name: 'Requirement 1' }) as HTMLInputElement).value).toBe('keep my draft');
    expect(screen.getByRole('button', { name: 'Confirm for first compile' })).toBeDefined();
    expect(screen.queryByText(/Latest Profile state was reloaded/)).toBeNull();
  });

  it('keeps guidance confirmation retryable after a transient failure', async () => {
    const profile = {
      ...emptyProfile('project-a', 1),
      requirements: [{ id: 'req-1', text: 'write concise summaries' }],
      derivation_status: 'ready' as const,
    };
    mocks.getProfile.mockResolvedValue(profile);
    mocks.getProfileBootstrapGuidance.mockResolvedValue({ bootstrap_guidance: bootstrapGuidance() });
    mocks.confirmProfileBootstrapGuidance
      .mockRejectedValueOnce(new Error('temporary network failure'))
      .mockResolvedValueOnce({ bootstrap_guidance: bootstrapGuidance('confirmed') });

    render(<ProjectProfilePanel projectId="project-a" />);
    fireEvent.click(await screen.findByRole('button', { name: 'Confirm for first compile' }));

    expect(await screen.findByText(/Guidance confirmation was not confirmed: temporary network failure/)).toBeDefined();
    fireEvent.click(screen.getByRole('button', { name: 'Confirm for first compile' }));

    expect(await screen.findByText(/Confirmed for first compile/)).toBeDefined();
    expect(mocks.confirmProfileBootstrapGuidance).toHaveBeenCalledTimes(2);
  });

  it('clears a superseded bootstrap preview after a requirements save', async () => {
    const profile = {
      ...emptyProfile('project-a', 1),
      requirements: [{ id: 'req-1', text: 'original requirement' }],
      derivation_status: 'ready' as const,
    };
    mocks.getProfile.mockResolvedValue(profile);
    mocks.getProfileBootstrapGuidance
      .mockResolvedValueOnce({ bootstrap_guidance: bootstrapGuidance() })
      .mockResolvedValueOnce({ bootstrap_guidance: null });

    render(<ProjectProfilePanel projectId="project-a" />);
    const input = await screen.findByRole('textbox', { name: 'Requirement 1' });
    expect(await screen.findByText('Prefer concrete, dated explanations.')).toBeDefined();
    fireEvent.change(input, { target: { value: 'updated requirement' } });
    expect((screen.getByRole('button', { name: 'Confirm for first compile' }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole('button', { name: 'Save requirements' }));

    expect(await screen.findByText(/Initial guidance preview is pending Profile derivation/)).toBeDefined();
    expect(screen.queryByText('Prefer concrete, dated explanations.')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Confirm for first compile' })).toBeNull();
    expect(mocks.confirmProfileBootstrapGuidance).not.toHaveBeenCalled();
  });

  it('polls pending derivation until the initial guidance preview is available', async () => {
    const pending = {
      ...emptyProfile('project-a', 3),
      requirements: [{ id: 'req-1', text: 'write concise summaries' }],
      derivation_status: 'pending' as const,
    };
    mocks.getProfile.mockResolvedValue(pending);
    mocks.getProfileBootstrapGuidance
      .mockResolvedValueOnce({ bootstrap_guidance: null })
      .mockResolvedValueOnce({ bootstrap_guidance: bootstrapGuidance('preview_ready', 3) });
    vi.useFakeTimers();

    render(<ProjectProfilePanel projectId="project-a" />);
    await act(async () => { await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); });
    expect(screen.getByText(/Initial guidance preview is pending Profile derivation/)).toBeDefined();
    await act(async () => { await vi.advanceTimersByTimeAsync(5000); });

    expect(screen.getByText('Prefer concrete, dated explanations.')).toBeDefined();
    expect(mocks.getProfileBootstrapGuidance).toHaveBeenCalledTimes(2);
  });

  it('shows separate manual preview diffs, keeps the active version visible, and retries only incomplete tagging', async () => {
    const profile = profileWithCandidate();
    mocks.getProfile.mockResolvedValue(profile);
    mocks.retryProfileCandidate.mockResolvedValue({
      ...profile,
      job: { ...profile.job, status: 'scheduled', missing_count: 2, error_code: null },
    });

    render(<ProjectProfilePanel projectId="project-a" />);

    expect(await screen.findByText('Added invoice tag')).toBeDefined();
    expect(screen.getByText('Prefer dated invoices')).toBeDefined();
    expect(screen.getByText(/Active profile: candidate-1/)).toBeDefined();
    expect(screen.getByText(/Preview awaiting confirmation/)).toBeDefined();
    fireEvent.click(screen.getByRole('button', { name: 'Retry missing tagging work' }));

    await waitFor(() => expect(mocks.retryProfileCandidate).toHaveBeenCalledWith('project-a', 'candidate-2', 6));
    expect(await screen.findByText(/Tagging work: Scheduled/)).toBeDefined();
  });

  it('requires an explicit confirmation and keeps the prior active Profile until tagging completes', async () => {
    const profile = profileWithCandidate();
    mocks.getProfile.mockResolvedValue(profile);
    mocks.confirmProfileCandidate.mockResolvedValue({
      ...profile,
      confirmed_candidate_id: 'candidate-2',
      job: { ...profile.job, status: 'running' },
    });

    render(<ProjectProfilePanel projectId="project-a" />);
    fireEvent.click(await screen.findByRole('button', { name: 'Confirm preview' }));

    await waitFor(() => expect(mocks.confirmProfileCandidate).toHaveBeenCalledWith('project-a', 'candidate-2', 6));
    expect(await screen.findByText(/Preview confirmed; waiting for tagging to finish/)).toBeDefined();
    expect(screen.getByText(/Active profile: candidate-1/)).toBeDefined();
  });

  it('notifies once when a confirmed candidate becomes active', async () => {
    const profile = profileWithCandidate();
    mocks.getProfile.mockResolvedValue(profile);
    mocks.confirmProfileCandidate.mockResolvedValue({
      ...profile,
      confirmed_candidate_id: 'candidate-2',
      active: {
        candidate_id: 'candidate-2',
        content_generation: 'generation-9',
        dictionary_revision: 'dict-2',
        tag_set_revision: 'tags-2',
        query_rule_revision: 'query-2',
        guidance_revision: 'guide-2',
      },
      job: { ...profile.job, status: 'ready', missing_count: 0, error_code: null },
    });

    render(<ProjectProfilePanel projectId="project-a" />);
    fireEvent.click(await screen.findByRole('button', { name: 'Confirm preview' }));

    expect(await screen.findByText('Profile candidate-2 is now active.')).toBeDefined();
    expect(screen.getAllByText('Profile candidate-2 is now active.')).toHaveLength(1);
  });

  it('retries a failed derivation with the current requirements revision', async () => {
    mocks.getProfile.mockResolvedValue({
      ...emptyProfile('project-a', 3),
      requirements: [{ id: 'req-1', text: 'invoice source' }],
      derivation_status: 'failed',
      derivation_error_code: 'derive_timeout',
    });
    mocks.retryProfileDerivation.mockResolvedValue({
      ...emptyProfile('project-a', 3),
      requirements: [{ id: 'req-1', text: 'invoice source' }],
      derivation_status: 'pending',
      scheduled_for: '2026-09-25T03:05:00Z',
    });

    render(<ProjectProfilePanel projectId="project-a" />);
    fireEvent.click(await screen.findByRole('button', { name: 'Retry preview generation' }));

    await waitFor(() => expect(mocks.retryProfileDerivation).toHaveBeenCalledWith('project-a', 3));
    expect(await screen.findByText(/scheduled by the server/i)).toBeDefined();
  });

  it('exposes exhausted Profile runtime work and retries it at the current revision', async () => {
    const active = {
      candidate_id: 'active-candidate',
      content_generation: 'generation-active',
      dictionary_revision: 'dict-active',
      tag_set_revision: 'tags-active',
      query_rule_revision: 'query-active',
      guidance_revision: 'guide-active',
    };
    const profile = {
      ...emptyProfile('project-a', 3),
      requirements: [{ id: 'req-1', text: 'invoice source' }],
      derivation_status: 'failed' as const,
      derivation_error_code: 'runtime_retry_exhausted',
      active,
    };
    mocks.getProfile.mockResolvedValue(profile);
    mocks.retryProfileDerivation.mockResolvedValue({
      ...profile,
      derivation_status: 'pending',
      derivation_error_code: null,
      scheduled_for: '2026-09-25T03:05:00Z',
    });

    render(<ProjectProfilePanel projectId="project-a" />);
    expect(await screen.findByText('Profile runtime work exhausted its automatic retries (runtime_retry_exhausted).')).toBeDefined();
    fireEvent.click(await screen.findByRole('button', { name: 'Retry Profile work' }));

    await waitFor(() => expect(mocks.retryProfileDerivation).toHaveBeenCalledWith('project-a', 3));
  });

  it('keeps requirement IDs and order when editing the original list', async () => {
    mocks.getProfile.mockResolvedValue({
      ...emptyProfile('project-a', 2),
      requirements: [
        { id: 'req-1', text: 'first' },
        { id: 'req-2', text: 'second' },
      ],
    });
    mocks.saveProfile.mockImplementation(async (_projectId: string, revision: number, requirements: ProfileState['requirements']) => ({
      ...emptyProfile('project-a', revision + 1),
      requirements,
      derivation_status: 'pending',
      scheduled_for: '2026-09-25T03:00:00Z',
    }));

    render(<ProjectProfilePanel projectId="project-a" />);
    fireEvent.click(await screen.findByRole('button', { name: 'Move requirement 2 up' }));
    fireEvent.click(screen.getByRole('button', { name: 'Remove requirement 2' }));
    fireEvent.click(screen.getByRole('button', { name: 'Save requirements' }));

    await waitFor(() => expect(mocks.saveProfile).toHaveBeenCalledWith('project-a', 2, [
      { id: 'req-2', text: 'second' },
    ]));
  });

  it('preserves the draft after a revision conflict and retries against the fetched latest revision', async () => {
    const latest = {
      ...emptyProfile('project-a', 5),
      requirements: [{ id: 'req-1', text: 'server version' }],
    };
    mocks.getProfile.mockResolvedValueOnce({
      ...emptyProfile('project-a', 4),
      requirements: [{ id: 'req-1', text: 'original' }],
    }).mockResolvedValueOnce(latest);
    mocks.saveProfile.mockRejectedValueOnce(Object.assign(new Error('revision conflict'), { status: 409 }));

    render(<ProjectProfilePanel projectId="project-a" />);
    const input = await screen.findByRole('textbox', { name: 'Requirement 1' });
    fireEvent.change(input, { target: { value: 'my draft' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save requirements' }));

    expect(await screen.findByText(/changed elsewhere/i)).toBeDefined();
    expect((screen.getByRole('textbox', { name: 'Requirement 1' }) as HTMLInputElement).value).toBe('my draft');
    fireEvent.click(screen.getByRole('button', { name: 'Save requirements' }));

    await waitFor(() => expect(mocks.saveProfile).toHaveBeenLastCalledWith(
      'project-a',
      5,
      [expect.objectContaining({ text: 'my draft' })],
    ));
  });

  it('does not let compile_auto candidates be manually confirmed', async () => {
    mocks.getProfile.mockResolvedValue(profileWithCandidate('compile_auto'));

    render(<ProjectProfilePanel projectId="project-a" />);

    expect(await screen.findByText(/Tags-only update from compile/)).toBeDefined();
    expect(screen.queryByRole('button', { name: 'Confirm preview' })).toBeNull();
    expect(mocks.confirmProfileCandidate).not.toHaveBeenCalled();
  });

  it('ignores a late Profile response after navigating to another project', async () => {
    const oldRequest = deferred<ReturnType<typeof emptyProfile>>();
    mocks.getProfile.mockImplementation((projectId: string) => (
      projectId === 'project-a' ? oldRequest.promise : Promise.resolve(emptyProfile(projectId))
    ));

    const view = render(<ProjectProfilePanel projectId="project-a" />);
    view.rerender(<ProjectProfilePanel projectId="project-b" />);
    oldRequest.resolve({ ...emptyProfile('project-a'), requirements: [{ id: 'stale', text: 'stale project content' }] });

    await screen.findByRole('textbox', { name: 'Requirement 1' }).catch(() => undefined);
    await waitFor(() => expect(mocks.getProfile).toHaveBeenCalledWith('project-b'));
    expect(screen.queryByText('stale project content')).toBeNull();
  });

  it('ignores a late bootstrap guidance response after switching projects', async () => {
    const oldGuidance = deferred<{ bootstrap_guidance: ProfileBootstrapGuidance }>();
    const oldProfile = {
      ...emptyProfile('project-a', 1),
      requirements: [{ id: 'req-1', text: 'old project requirement' }],
      derivation_status: 'ready' as const,
    };
    mocks.getProfile.mockImplementation((projectId: string) => Promise.resolve(
      projectId === 'project-a' ? oldProfile : emptyProfile(projectId),
    ));
    mocks.getProfileBootstrapGuidance.mockReturnValue(oldGuidance.promise);

    const view = render(<ProjectProfilePanel projectId="project-a" />);
    expect(await screen.findByText(/Checking for the initial guidance preview/)).toBeDefined();
    view.rerender(<ProjectProfilePanel projectId="project-b" />);
    await screen.findByRole('textbox', { name: 'Requirement 1' });
    oldGuidance.resolve({ bootstrap_guidance: bootstrapGuidance('preview_ready', 1) });

    await waitFor(() => expect(mocks.getProfile).toHaveBeenCalledWith('project-b'));
    expect(screen.queryByText('Prefer concrete, dated explanations.')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Confirm for first compile' })).toBeNull();
  });

  it('ignores a late bootstrap confirmation response after an account switch', async () => {
    const deferredConfirmation = deferred<{ bootstrap_guidance: ProfileBootstrapGuidance }>();
    const accountAProfile = {
      ...emptyProfile('project-a', 1),
      requirements: [{ id: 'req-1', text: 'account A requirement' }],
      derivation_status: 'ready' as const,
    };
    const AccountScopedPanel = ({ accountId }: { accountId: string }) => (
      <ProjectProfilePanel key={`${accountId}:project-a`} projectId="project-a" />
    );
    mocks.getProfile.mockResolvedValueOnce(accountAProfile).mockResolvedValueOnce(emptyProfile('project-a'));
    mocks.getProfileBootstrapGuidance.mockResolvedValue({ bootstrap_guidance: bootstrapGuidance() });
    mocks.confirmProfileBootstrapGuidance.mockReturnValue(deferredConfirmation.promise);

    const view = render(<AccountScopedPanel accountId="account-a" />);
    fireEvent.click(await screen.findByRole('button', { name: 'Confirm for first compile' }));
    view.rerender(<AccountScopedPanel accountId="account-b" />);
    await screen.findByRole('textbox', { name: 'Requirement 1' });
    deferredConfirmation.resolve({ bootstrap_guidance: bootstrapGuidance('confirmed') });

    expect(screen.queryByText(/Confirmed for first compile/)).toBeNull();
    expect(screen.queryByText('Prefer concrete, dated explanations.')).toBeNull();
  });

  it('keeps polling after a ready job when the Profile refresh fails once', async () => {
    const pending = {
      ...profileWithCandidate(),
      confirmed_candidate_id: 'candidate-2',
      job: { ...profileWithCandidate().job, status: 'running' as const },
    };
    const active = {
      ...pending,
      active: {
        candidate_id: 'candidate-2',
        content_generation: 'generation-9',
        dictionary_revision: 'dict-2',
        tag_set_revision: 'tags-2',
        query_rule_revision: 'query-2',
        guidance_revision: 'guide-2',
      },
      job: { ...pending.job, status: 'ready' as const, missing_count: 0, error_code: null },
    };
    mocks.getProfile.mockResolvedValueOnce(pending)
      .mockRejectedValueOnce(new Error('temporary read failure'))
      .mockResolvedValueOnce(active);
    mocks.getProfileJob.mockResolvedValue({ ...pending.job, status: 'ready', missing_count: 0, error_code: null });
    vi.useFakeTimers();

    render(<ProjectProfilePanel projectId="project-a" />);
    await act(async () => { await Promise.resolve(); await Promise.resolve(); });
    expect(screen.getByText(/Tagging work: Running/)).toBeDefined();

    await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
    expect(screen.getByText(/Active profile: candidate-1/)).toBeDefined();
    await act(async () => { await vi.advanceTimersByTimeAsync(5000); });

    expect(screen.getByText(/Active profile: candidate-2/)).toBeDefined();
    expect(screen.getByText('Profile candidate-2 is now active.')).toBeDefined();
    expect(mocks.getProfile).toHaveBeenCalledTimes(3);
  });

  it('ignores an in-flight save response after switching to another project', async () => {
    const deferredSave = deferred<ProfileState>();
    mocks.getProfile.mockImplementation((projectId: string) => Promise.resolve(emptyProfile(projectId)));
    mocks.saveProfile.mockReturnValue(deferredSave.promise);

    const view = render(<ProjectProfilePanel projectId="project-a" />);
    fireEvent.change(await screen.findByRole('textbox', { name: 'Requirement 1' }), { target: { value: 'stale save' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save requirements' }));
    view.rerender(<ProjectProfilePanel projectId="project-b" />);
    await screen.findByRole('textbox', { name: 'Requirement 1' });
    deferredSave.resolve({ ...emptyProfile('project-a', 1), requirements: [{ id: 'req-a', text: 'stale save' }] });

    await waitFor(() => expect(mocks.getProfile).toHaveBeenCalledWith('project-b'));
    expect(screen.queryByText('stale save')).toBeNull();
    expect(screen.queryByText(/Profile req-a is now active/)).toBeNull();
  });

  it('does not issue a follow-up GET after a stale save rejection', async () => {
    const deferredSave = deferred<ProfileState>();
    mocks.getProfile.mockImplementation((projectId: string) => Promise.resolve(emptyProfile(projectId)));
    mocks.saveProfile.mockReturnValue(deferredSave.promise);

    const view = render(<ProjectProfilePanel projectId="project-a" />);
    fireEvent.change(await screen.findByRole('textbox', { name: 'Requirement 1' }), { target: { value: 'stale save' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save requirements' }));
    view.rerender(<ProjectProfilePanel projectId="project-b" />);
    await screen.findByRole('textbox', { name: 'Requirement 1' });
    await act(async () => {
      deferredSave.reject(Object.assign(new Error('revision conflict'), { status: 409 }));
      await Promise.resolve();
    });

    expect(mocks.getProfile.mock.calls.filter(([id]) => id === 'project-a')).toHaveLength(1);
    expect(screen.queryByText(/changed elsewhere/)).toBeNull();
    expect(screen.queryByText('stale save')).toBeNull();
  });

  it('ignores an in-flight confirmation response after switching to another project', async () => {
    const deferredConfirmation = deferred<ProfileState>();
    mocks.getProfile.mockImplementation((projectId: string) => Promise.resolve(
      projectId === 'project-a' ? profileWithCandidate() : emptyProfile(projectId),
    ));
    mocks.confirmProfileCandidate.mockReturnValue(deferredConfirmation.promise);

    const view = render(<ProjectProfilePanel projectId="project-a" />);
    fireEvent.click(await screen.findByRole('button', { name: 'Confirm preview' }));
    view.rerender(<ProjectProfilePanel projectId="project-b" />);
    await screen.findByRole('textbox', { name: 'Requirement 1' });
    deferredConfirmation.resolve({
      ...profileWithCandidate(),
      active: {
        candidate_id: 'candidate-2',
        content_generation: 'generation-9',
        dictionary_revision: 'dict-2',
        tag_set_revision: 'tags-2',
        query_rule_revision: 'query-2',
        guidance_revision: 'guide-2',
      },
    });

    await waitFor(() => expect(mocks.getProfile).toHaveBeenCalledWith('project-b'));
    expect(screen.queryByText(/Profile candidate-2 is now active/)).toBeNull();
    expect(screen.queryByText('Added invoice tag')).toBeNull();
  });

  it('omits the full-project recompile UI and makes no capability request', async () => {
    render(<ProjectProfilePanel projectId="project-a" />);

    await screen.findByRole('textbox', { name: 'Requirement 1' });
    expect(screen.queryByRole('button', { name: /Recompile all/i })).toBeNull();
    expect(mocks.getRecompileAllCapability).not.toHaveBeenCalled();
    expect(mocks.recompileAll).not.toHaveBeenCalled();
  });

  it('renders the Profile panel in Traditional Chinese', async () => {
    localStorage.setItem('locale', 'zh-TW');
    render(<ProjectProfilePanel projectId="project-a" />);

    expect(await screen.findByText('原始需求')).toBeDefined();
    expect(screen.getByRole('textbox', { name: '需求 1' })).toBeDefined();
    expect(screen.queryByText('Original requirements')).toBeNull();
  });
});
