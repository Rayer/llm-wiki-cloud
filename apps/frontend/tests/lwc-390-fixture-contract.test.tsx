import { beforeEach, expect, it } from 'vitest';
import {
  confirmProfileBootstrapGuidance,
  getProfile,
  getProfileBootstrapGuidance,
  saveProfile,
} from '@/lib/api';
import './fixtures/lwc-390-profile-fixture';

type FixtureScenario = 'active-pending' | 'active-idle' | 'first-compile';
type FixtureWindow = Window & {
  __lwc390Fixture?: {
    scenario: FixtureScenario;
    writes: number;
    profiles: Record<string, unknown>;
    bootstrapGuidance: Record<string, unknown>;
  };
};

const fixture = (window as FixtureWindow).__lwc390Fixture!;

beforeEach(() => {
  fixture.scenario = 'active-idle';
  fixture.writes = 0;
  fixture.profiles = {};
  fixture.bootstrapGuidance = {};
});

it('reads saved requirements and revision from GET after the SDK PUT', async () => {
  const projectId = 'lwc390-fixture-contract-save';
  const current = await getProfile(projectId);
  const requirements = current.requirements.map((requirement) => ({
    ...requirement,
    text: 'Keep this saved requirement available to later reads.',
  }));

  const saved = await saveProfile(projectId, current.revision, requirements);
  const readback = await getProfile(projectId);

  expect(saved.revision).toBe(current.revision + 1);
  expect(readback.revision).toBe(saved.revision);
  expect(readback.requirements).toEqual(requirements);
  expect(fixture.writes).toBe(1);
});

it('reads confirmed bootstrap guidance after confirmation while Profile remains inactive', async () => {
  fixture.scenario = 'first-compile';
  const projectId = 'lwc390-fixture-contract-bootstrap';
  const profile = await getProfile(projectId);
  const preview = await getProfileBootstrapGuidance(projectId);
  const guidance = preview.bootstrap_guidance;
  expect(guidance?.status).toBe('preview_ready');

  const confirmation = await confirmProfileBootstrapGuidance(
    projectId,
    guidance!.revision,
    profile.revision,
    guidance!.input_digest,
  );
  const readback = await getProfileBootstrapGuidance(projectId);
  const currentProfile = await getProfile(projectId);

  expect(confirmation.bootstrap_guidance?.status).toBe('confirmed');
  expect(readback.bootstrap_guidance).toMatchObject({
    revision: guidance!.revision,
    profile_revision: profile.revision,
    status: 'confirmed',
    confirmed_at: '2030-01-01T00:00:00Z',
  });
  expect(currentProfile.active).toBeNull();
  expect(currentProfile.revision).toBe(profile.revision);
  expect(fixture.writes).toBe(1);
});
