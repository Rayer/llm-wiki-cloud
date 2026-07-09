import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

test('pipeline client supports multi-file raw upload queue', async () => {
  const source = await readFile(
    new URL('../src/components/PipelineClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(source, /multiple/);
  assert.match(source, /UPLOAD_CONCURRENCY\s*=\s*3/);
  assert.match(source, /already_exists/);
  assert.match(source, /upload-summary/);
  assert.match(source, /duplicate filename in batch/);
  assert.match(source, /Retry/);
  assert.match(source, /uploadRawFile/);
});

test('pipeline client still polls status after accepted pipeline run', async () => {
  const pipelineClient = await readFile(
    new URL('../src/components/PipelineClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(pipelineClient, /getPipelineStatus/);
  assert.match(pipelineClient, /result\.status === 'accepted'/);
  assert.match(pipelineClient, /setInterval\([^,]+,\s*5000\)/s);
});
