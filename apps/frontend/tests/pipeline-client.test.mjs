import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

test('pipeline client polls status after an accepted pipeline run', async () => {
  const pipelineClient = await readFile(
    new URL('../src/components/PipelineClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(pipelineClient, /getPipelineStatus/);
  assert.match(pipelineClient, /result\.status === 'accepted'/);
  assert.match(pipelineClient, /setInterval\([^,]+,\s*5000\)/s);
  assert.match(pipelineClient, /Pipeline running\.\.\./);
  assert.match(pipelineClient, /Pipeline complete/);
  assert.match(pipelineClient, /Pipeline failed/);
  assert.match(pipelineClient, /duration/);
  assert.match(pipelineClient, /clearInterval/);
});

test('pipeline client resumes the persisted intent and derives Cloud Run state from evidence', async () => {
  const pipelineClient = await readFile(
    new URL('../src/components/PipelineClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(pipelineClient, /executionStatus === 'RUNNING'/);
  assert.match(pipelineClient, /resumableIntent = status\.pipeline_intent \?\? null/);
  assert.match(pipelineClient, /isPendingPipelineIntent\(resumableIntent\)/);
  assert.match(pipelineClient, /pollPipelineStatus\(resumedScope, intent\.execution_id, intent\.id\)/);
  assert.doesNotMatch(pipelineClient, /status: 'RUNNING'/);
});

test('pipeline client shows accepted intent errors and retries the frozen run', async () => {
  const pipelineClient = await readFile(
    new URL('../src/components/PipelineClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(pipelineClient, /Pipeline\.intentRetry/);
  assert.match(pipelineClient, /pipelineStatus\.pipeline_intent\.error_message/);
  assert.match(pipelineClient, /retryPipeline\(intent\.id, projectId\)/);
  assert.match(pipelineClient, /prepared_guidance/);
});

test('pipeline client disables repeat runs while the accepted intent is pending', async () => {
  const pipelineClient = await readFile(
    new URL('../src/components/PipelineClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(pipelineClient, /pipelineIntentPending = \['preparing', 'ready', 'invoking', 'invocation_unknown', 'compiling'\]/);
  assert.match(pipelineClient, /const blocked = pipelineIntentPending \|\|/);
});
