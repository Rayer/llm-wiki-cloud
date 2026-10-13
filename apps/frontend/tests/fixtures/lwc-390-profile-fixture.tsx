'use client';

import { useEffect, useRef, useState } from 'react';
import { ProjectProfilePanel } from '@/components/ProjectProfilePanel';
import { RuntimeConfigBoundary } from '@/components/RuntimeConfigBoundary';
import { configureApiAuth, type ProfileState } from '@/lib/api';
import { useT } from '@/lib/i18n';

type Scenario = 'active-pending' | 'active-idle' | 'first-compile';
type FixtureState = { scenario: Scenario; writes: number };
type FixtureWindow = Window & { __lwc390Fixture?: FixtureState };

const fixtureState = typeof window === 'undefined'
  ? { scenario: 'active-pending' as Scenario, writes: 0 }
  : ((window as FixtureWindow).__lwc390Fixture ??= { scenario: 'active-pending', writes: 0 });

if (typeof window !== 'undefined') {
  window.localStorage.setItem('locale', 'zh-TW');
  configureApiAuth({
    getAccessToken: () => 'fixture-placeholder',
    refreshAccessToken: async () => null,
    onUnauthorized: () => undefined,
  });

  window.fetch = async (input, init) => {
    const url = new URL(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url);
    const method = init?.method ?? (input instanceof Request ? input.method : 'GET');
    const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), {
      status,
      headers: { 'Content-Type': 'application/json' },
    });

    if (url.pathname === '/frontend-config.json') {
      return json({
        schema_version: 1,
        api_url: 'http://127.0.0.1:4173',
        auth_url: 'http://127.0.0.1:4173',
      });
    }

    const match = url.pathname.match(/^\/api\/v1\/projects\/([^/]+)\/profile(.*)$/);
    if (!match) throw new Error(`Blocked by the local Profile fixture: ${url.pathname}`);

    const projectId = decodeURIComponent(match[1]);
    const suffix = match[2];
    const profile = createProfile(projectId, fixtureState.scenario);

    if (method === 'GET' && suffix === '') return json(profile);
    if (method === 'GET' && suffix === '/bootstrap-guidance') {
      return json({ bootstrap_guidance: fixtureState.scenario === 'first-compile' ? bootstrapGuidance(profile.revision) : null });
    }
    if (method === 'GET' && suffix.startsWith('/guidance/')) {
      const revision = decodeURIComponent(suffix.slice('/guidance/'.length));
      return json({ guidance_artifact: {
        revision,
        input_digest: `fixture-${revision}`,
        model_version: 'fixture-model-v1',
        prompt_version: 'fixture-prompt-v1',
        schema_version: 'fixture-guidance.v1',
        compile_guidance: 'Preserve source-linked details; keep unknown information unknown.',
      } });
    }
    if (method === 'GET' && suffix === '/jobs/fixture-job') {
      return json({ job_id: 'fixture-job', candidate_id: 'fixture-candidate-2', content_generation: 'fixture-generation-2', status: 'running', missing_count: 2, error_code: null });
    }
    if (method === 'PUT' && suffix === '') {
      fixtureState.writes += 1;
      window.dispatchEvent(new CustomEvent('lwc390:mock-write', { detail: fixtureState.writes }));
      const payload = JSON.parse(String(init?.body ?? '{}')) as { expected_revision?: number; requirements?: ProfileState['requirements'] };
      return json({
        ...profile,
        revision: (payload.expected_revision ?? profile.revision) + 1,
        requirements: payload.requirements ?? profile.requirements,
        derivation_status: 'pending',
        scheduled_for: '2030-01-01T00:00:00Z',
        candidate: null,
        confirmed_candidate_id: null,
      });
    }
    if (method === 'POST' && /\/bootstrap-guidance\/[^/]+\/confirm$/.test(suffix)) {
      fixtureState.writes += 1;
      window.dispatchEvent(new CustomEvent('lwc390:mock-write', { detail: fixtureState.writes }));
      const guidance = bootstrapGuidance(profile.revision);
      return json({ bootstrap_guidance: { ...guidance, status: 'confirmed', confirmed_at: '2030-01-01T00:00:00Z' } });
    }

    throw new Error(`Blocked by the local Profile fixture: ${method} ${url.pathname}`);
  };
}

function createProfile(projectId: string, scenario: Scenario): ProfileState {
  const bootstrap = scenario === 'first-compile';
  return {
    project_id: projectId,
    revision: bootstrap ? 3 : 4,
    requirements: [{ id: 'fixture-req-1', text: 'Keep source evidence and leave unmentioned details unknown.' }],
    derivation_status: 'ready',
    scheduled_for: null,
    derivation_error_code: null,
    candidate: bootstrap ? null : {
      candidate_id: 'fixture-candidate-2',
      source: 'manual',
      base_revision: 4,
      requirements_digest: 'fixture-digest-2',
      content_generation: 'fixture-generation-2',
      dictionary: { revision: 'fixture-dictionary-2', input_digest: 'fixture-digest-2', model_version: 'fixture-model-v1', prompt_version: 'fixture-prompt-v1', schema_version: 'fixture-dictionary.v1' },
      guidance: { revision: 'fixture-guidance-2', input_digest: 'fixture-digest-2', model_version: 'fixture-model-v1', prompt_version: 'fixture-prompt-v1', schema_version: 'fixture-guidance.v1' },
      preview: { dictionary_diff: 'Added source detail and parking terms.', guidance_diff: 'Keep the supporting evidence visible.', requirements: [{ id: 'fixture-req-1', disposition: 'both', explanation: 'Applied to the controlled fixture only.' }] },
    },
    confirmed_candidate_id: bootstrap ? null : 'fixture-candidate-2',
    active: bootstrap ? null : {
      candidate_id: 'fixture-candidate-1',
      content_generation: 'fixture-generation-1',
      dictionary_revision: 'fixture-dictionary-1',
      tag_set_revision: 'fixture-tags-1',
      query_rule_revision: 'fixture-query-1',
      guidance_revision: 'fixture-guidance-1',
    },
    job: bootstrap || scenario === 'active-idle' ? null : {
      job_id: 'fixture-job',
      candidate_id: 'fixture-candidate-2',
      content_generation: 'fixture-generation-2',
      status: 'running',
      missing_count: 2,
      error_code: null,
    },
  };
}

function bootstrapGuidance(profileRevision: number) {
  return {
    revision: 'fixture-bootstrap-guidance-1',
    input_digest: 'fixture-bootstrap-digest-1',
    profile_revision: profileRevision,
    status: 'preview_ready' as const,
    model_version: 'fixture-model-v1',
    prompt_version: 'fixture-prompt-v1',
    schema_version: 'fixture-bootstrap-guidance.v1',
    confirmed_at: null,
    preview: {
      guidance_diff: 'A first-compile fixture preview for interaction checks.',
      requirements: [{ id: 'fixture-req-1', disposition: 'compile_guidance' as const, explanation: 'Synthetic response intercepted in this browser.' }],
    },
  };
}

function FixtureControls({ onScenario }: { onScenario: (scenario: Scenario) => void }) {
  const { setLocale } = useT();
  const [writes, setWrites] = useState(fixtureState.writes);
  const [controlsOpen, setControlsOpen] = useState(false);
  const geometryRef = useRef<HTMLOutputElement>(null);

  useEffect(() => {
    const onMockWrite = (event: Event) => setWrites((event as CustomEvent<number>).detail);
    const measure = () => {
      const viewport = window.innerWidth;
      const document = documentRefWidth();
      if (geometryRef.current) geometryRef.current.textContent = `viewport=${viewport}px document=${document}px overflow=${document > viewport}`;
    };
    window.addEventListener('lwc390:mock-write', onMockWrite);
    window.addEventListener('resize', measure);
    measure();
    return () => {
      window.removeEventListener('lwc390:mock-write', onMockWrite);
      window.removeEventListener('resize', measure);
    };
  }, []);

  return (
    <section className="mb-3 rounded-lg border border-fuchsia-300/40 bg-fuchsia-950/30 p-2 text-xs" aria-label="Local fixture controls">
      <button type="button" aria-expanded={controlsOpen} onClick={() => setControlsOpen((open) => !open)} className="min-h-11 w-full rounded px-2 text-left font-semibold text-fuchsia-100">
        LOCAL FIXTURE ONLY · all API responses are intercepted in this browser
      </button>
      <div hidden={!controlsOpen} className="px-1 pb-1">
        <div className="mt-2 flex flex-wrap gap-2">
          <button type="button" className="min-h-11 rounded border border-white/20 px-3" onClick={() => setLocale('zh-TW')}>繁體中文</button>
          <button type="button" className="min-h-11 rounded border border-white/20 px-3" onClick={() => setLocale('en')}>English</button>
          <button type="button" className="min-h-11 rounded border border-white/20 px-3" onClick={() => onScenario('active-pending')}>Active + pending candidate</button>
          <button type="button" className="min-h-11 rounded border border-white/20 px-3" onClick={() => onScenario('active-idle')}>Active · no pending work</button>
          <button type="button" className="min-h-11 rounded border border-white/20 px-3" onClick={() => onScenario('first-compile')}>No active · first compile</button>
        </div>
        <p className="mt-2 text-fuchsia-100/80">Mock API writes: <output data-testid="mock-api-writes">{writes}</output></p>
        <output className="block text-fuchsia-100/80" data-testid="fixture-scenario">{fixtureState.scenario}</output>
        <output ref={geometryRef} className="mt-1 block text-fuchsia-100/80" data-testid="viewport-geometry">measuring viewport</output>
      </div>
    </section>
  );
}

function documentRefWidth(): number {
  return Math.max(document.documentElement.scrollWidth, document.body.scrollWidth);
}

export default function LWC390ProfileFixture() {
  const [scenario, setScenario] = useState<Scenario>(fixtureState.scenario);
  const projectId = scenario === 'first-compile' ? 'lwc390-fixture-bootstrap'
    : scenario === 'active-idle' ? 'lwc390-fixture-idle'
      : 'lwc390-fixture-active';

  function changeScenario(next: Scenario) {
    fixtureState.scenario = next;
    setScenario(next);
    window.localStorage.setItem('llm-wiki-last-project', next === 'first-compile' ? 'lwc390-fixture-bootstrap' : 'lwc390-fixture-active');
  }

  return (
    <RuntimeConfigBoundary>
      <main className="mx-auto w-full max-w-5xl px-3 py-4 sm:px-6 sm:py-8">
        <FixtureControls onScenario={changeScenario} />
        <ProjectProfilePanel key={projectId} projectId={projectId} />
      </main>
    </RuntimeConfigBoundary>
  );
}
