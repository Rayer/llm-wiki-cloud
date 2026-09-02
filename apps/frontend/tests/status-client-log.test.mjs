import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

import {
  beginPipelineLogRequest,
  completePipelineLogRequest,
  failPipelineLogRequest,
  getPipelineLogAvailability,
  initialPipelineLogState,
} from '../src/lib/status-log.ts';
import { getPipelineTimelineState } from '../src/lib/pipeline-timeline.ts';

test('status client wiring keeps log access behind the explicit open handler', async () => {
  const statusClient = await readFile(
    new URL('../src/components/StatusClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(statusClient, /getStatus/);
  assert.equal((statusClient.match(/getPipelineLog\(/g) ?? []).length, 1);
  assert.match(statusClient, /getPipelineLog\(logUrl, projectId\)/);
  assert.doesNotMatch(statusClient, /getPipelineStatus/);
  const handlerStart = statusClient.indexOf('const onOpenLog');
  const handlerEnd = statusClient.indexOf('\n\n  return (', handlerStart);
  assert.ok(handlerStart >= 0 && handlerEnd > handlerStart);
  assert.match(statusClient.slice(handlerStart, handlerEnd), /getPipelineLog\(/);
  assert.match(statusClient, /LOG_PREVIEW_BYTES\s*=\s*10 \* 1024/);
  assert.match(statusClient, /LOG_PREVIEW_LINES\s*=\s*50/);
  assert.match(statusClient, /slice\(-LOG_PREVIEW_LINES\)/);
  assert.match(statusClient, /overflow-y-auto/);
  assert.match(statusClient, /<pre/);
  assert.match(statusClient, /showFullLog/);
});

test('status client recognizes Cloud Run uppercase running status', async () => {
  const statusClient = await readFile(
    new URL('../src/components/StatusClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(statusClient, /execStatus === 'RUNNING'/);
});

test('status wiring reloads by project and keeps metadata loading separate from log opening', async () => {
  const statusClient = await readFile(
    new URL('../src/components/StatusClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(statusClient, /currentProject\?\.id/);
  assert.match(statusClient, /\}, \[projectId\]\);/);
  assert.match(statusClient, /onOpenLog/);
  assert.match(statusClient, /onClick=\{onOpenLog\}/);
  assert.match(statusClient, /getStatus\(projectId\)/);
  assert.match(statusClient, /disabled=\{isLoading\}/);
  assert.match(statusClient, /aria-busy=\{isLoading\}/);
  assert.match(statusClient, /role="status" aria-live="polite"/);
  assert.match(statusClient, /role="alert"/);
});

test('pipeline log states gate fetching and expose typed diagnostic rendering', async () => {
  const statusClient = await readFile(
    new URL('../src/components/StatusClient.tsx', import.meta.url),
    'utf8',
  );
  const logState = await readFile(new URL('../src/lib/status-log.ts', import.meta.url), 'utf8');

  assert.match(logState, /available/);
  assert.match(logState, /pending/);
  assert.match(logState, /unavailable/);
  assert.match(logState, /missing/);
  assert.match(statusClient, /error_class/);
  assert.match(statusClient, /detail_code/);
  assert.match(statusClient, /child_command/);
  assert.match(statusClient, /exit_code/);
  assert.match(statusClient, /stage unavailable/);
  assert.doesNotMatch(statusClient, /isFailed\s*\?\s*2/);
});

test('pipeline log availability is fail-closed with finite reason messages', () => {
  const cases = [
    [{ log_state: 'pending' }, 'pending', 'Pipeline log is still pending.', false],
    [{ log_state: 'unavailable', log_state_reason: 'unsupported_execution_status' }, 'unavailable', 'Pipeline log is unavailable for this execution status.', false],
    [{ log_state: 'unavailable', log_state_reason: 'storage_unavailable' }, 'unavailable', 'Pipeline log storage is unavailable.', false],
    [{ log_state: 'unavailable', log_state_reason: 'log_unavailable' }, 'unavailable', 'Pipeline log is unavailable.', false],
    [{ log_state: 'unavailable', log_state_reason: 'log_too_large' }, 'unavailable', 'Pipeline log is too large to display.', false],
    [{ log_state: 'unavailable', log_state_reason: 'attacker-controlled text' }, 'unavailable', 'Pipeline log is unavailable.', false],
    [{ log_state: 'unknown_future', log_url: '/stale' }, 'unavailable', 'Pipeline log is unavailable.', false],
    [{ log_state: 'pending', log_url: '/pending' }, 'pending', 'Pipeline log is still pending.', false],
    [{ log_state: 'unavailable', log_url: '/unavailable' }, 'unavailable', 'Pipeline log is unavailable.', false],
    [{ log_state: 'missing', log_url: '/missing' }, 'missing', 'No pipeline log is available.', false],
    [{ log_url: '/available' }, 'available', null, true],
    [{ log_state: 'available' }, 'unavailable', 'Pipeline log is unavailable.', false],
    [{}, 'missing', 'No pipeline log is available.', false],
  ];

  for (const [execution, state, message, canOpen] of cases) {
    assert.deepEqual(getPipelineLogAvailability(execution), {
      state,
      message,
      canOpen,
    });
  }
});

test('pipeline log state distinguishes unopened, loading, loaded-empty, loaded-nonempty, and error', () => {
  const identity = { projectId: 'project-a', executionId: 'execution-a', logUrl: '/log-a', nonce: 1 };
  let state = initialPipelineLogState('project-a');
  let fetchCount = 0;

  const explicitClick = () => {
    const next = beginPipelineLogRequest(state, identity);
    if (next !== state) fetchCount += 1;
    state = next;
  };

  assert.equal(state.phase, 'never-opened');
  explicitClick();
  explicitClick();
  assert.equal(fetchCount, 1);
  assert.equal(state.phase, 'loading');

  state = completePipelineLogRequest(state, identity, '');
  assert.deepEqual(state, {
    phase: 'loaded-empty',
    projectId: 'project-a',
    identity,
    text: '',
    error: '',
  });
  assert.equal(beginPipelineLogRequest(state, identity), state);

  state = initialPipelineLogState('project-a');
  state = beginPipelineLogRequest(state, identity);
  state = failPipelineLogRequest(state, identity, 'Pipeline log request failed (503)');
  assert.equal(state.phase, 'error');
  assert.equal(beginPipelineLogRequest(state, identity).phase, 'loading');

  state = completePipelineLogRequest(
    beginPipelineLogRequest(initialPipelineLogState('project-a'), identity),
    identity,
    'line 1',
  );
  assert.equal(state.phase, 'loaded-nonempty');
});

test('pipeline log rejects stale project and execution requests', () => {
  const identityA = { projectId: 'project-a', executionId: 'execution-a', logUrl: '/log-a', nonce: 1 };
  const identityB = { projectId: 'project-b', executionId: 'execution-b', logUrl: '/log-b', nonce: 2 };
  let state = beginPipelineLogRequest(initialPipelineLogState('project-a'), identityA);
  state = beginPipelineLogRequest(initialPipelineLogState('project-b'), identityB);

  assert.deepEqual(completePipelineLogRequest(state, identityA, 'project A log'), state);
  assert.deepEqual(failPipelineLogRequest(state, identityA, 'project A error'), state);
  assert.equal(completePipelineLogRequest(state, identityB, 'project B log').text, 'project B log');
});

test('typed diagnostics select only the reported known failed stage', () => {
  assert.deepEqual(
    getPipelineTimelineState({
      status: 'FAILED',
      diagnostic: { stage: 'concept_reconciliation' },
    }),
    { completedSteps: 0, failedStep: null, stageLabel: 'Concept reconciliation' },
  );
  const concreteStages = {
    input_materialization: 'Input materialization',
    synto_migration: 'Synto migration',
    synto_config_normalization: 'Synto config normalization',
    synto_config_validation: 'Synto config validation',
    synto_run: 'Synto run',
    synto_index_export: 'Synto index export',
    source_reconciliation: 'Source reconciliation',
    concept_reconciliation: 'Concept reconciliation',
    postprocess: 'Postprocess',
    generation_publish: 'Generation publish',
    receipt_recording: 'Receipt recording',
    lease_cleanup: 'Lease cleanup',
  };
  for (const [stage, label] of Object.entries(concreteStages)) {
    assert.deepEqual(
      getPipelineTimelineState({ status: 'FAILED', diagnostic: { stage } }),
      { completedSteps: 0, failedStep: null, stageLabel: label },
    );
  }
  assert.equal(getPipelineTimelineState({ status: 'FAILED' }).stageLabel, 'stage unavailable');
  assert.equal(getPipelineTimelineState({ status: 'FAILED', diagnostic: { stage: 'unknown' } }).stageLabel, 'stage unavailable');
  assert.equal(getPipelineTimelineState({ status: 'FAILED', diagnostic: { stage: 'future_stage' } }).stageLabel, 'stage unavailable');
  assert.equal(getPipelineTimelineState({ status: 'RUNNING' }).completedSteps, 1);
  assert.equal(getPipelineTimelineState({ status: 'SUCCEEDED' }).completedSteps, 4);
});
