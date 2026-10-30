import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

test('Home and Raw share the multi-file raw upload queue', async () => {
  const [pipeline, uploadPanel] = await Promise.all([
    readFile(new URL('../src/components/PipelineClient.tsx', import.meta.url), 'utf8'),
    readFile(new URL('../src/components/RawUploadPanel.tsx', import.meta.url), 'utf8'),
  ]);

  assert.match(pipeline, /<RawUploadPanel/);
  assert.match(uploadPanel, /multiple/);
  assert.match(uploadPanel, /UPLOAD_CONCURRENCY\s*=\s*3/);
  assert.match(uploadPanel, /already_exists/);
  assert.match(uploadPanel, /upload-summary/);
  assert.match(uploadPanel, /RawUpload\.duplicateFilenameInBatch/);
  assert.match(uploadPanel, /Retry/);
  assert.match(uploadPanel, /uploadRawFile/);
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
