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

test('pipeline client uses Cloud Run uppercase running status', async () => {
  const pipelineClient = await readFile(
    new URL('../src/components/PipelineClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(pipelineClient, /executionStatus === 'RUNNING'/);
  assert.match(pipelineClient, /status: 'RUNNING'/);
});
