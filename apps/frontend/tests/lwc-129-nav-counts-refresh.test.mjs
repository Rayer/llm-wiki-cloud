import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

test('workspace provider owns nav counts and refreshNavCounts', async () => {
  const provider = await readFile(
    new URL('../src/components/WorkspaceProvider.tsx', import.meta.url),
    'utf8',
  );

  assert.match(provider, /refreshNavCounts/);
  assert.match(provider, /navCounts/);
  assert.match(provider, /getStatus/);
  assert.match(provider, /displayedNavCounts/);
});

test('shell reads nav counts from workspace instead of local getStatus', async () => {
  const shell = await readFile(
    new URL('../src/components/Shell.tsx', import.meta.url),
    'utf8',
  );

  assert.match(shell, /navCounts/);
  assert.match(shell, /useWorkspace\(\)/);
  assert.match(shell, /getBuildInfo/);
  assert.doesNotMatch(shell, /getStatus\(\)/);
});

test('pipeline client refreshes nav counts after created uploads', async () => {
  const [pipeline, uploadPanel] = await Promise.all([
    readFile(new URL('../src/components/PipelineClient.tsx', import.meta.url), 'utf8'),
    readFile(new URL('../src/components/RawUploadPanel.tsx', import.meta.url), 'utf8'),
  ]);

  assert.match(pipeline, /onQueueSettled=\{handleUploadQueueSettled\}/);
  assert.match(pipeline, /handleUploadQueueSettled[\s\S]*?void refreshNavCounts\(\)[\s\S]*?refreshPipelineStatus/);
  assert.match(uploadPanel, /changedProjectIdsRef\.current\.add\(next\.projectId\)/);
  assert.match(uploadPanel, /if \(onQueueSettledRef\.current\) onQueueSettledRef\.current\(result\);[\s\S]*?else void refreshNavCounts\(\)/);
  assert.match(uploadPanel, /rawResult\?\.status === 'created'/);
  assert.match(uploadPanel, /if \(record\.source_state === 'saved'\)[\s\S]*?changedProjectIdsRef\.current\.add\(item\.projectId\)[\s\S]*?void refreshNavCounts\(\)/);
});
