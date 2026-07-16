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
  assert.doesNotMatch(shell, /from '@\/lib\/api'/);
  assert.doesNotMatch(shell, /getStatus\(\)/);
});

test('pipeline client refreshes nav counts after created uploads', async () => {
  const pipeline = await readFile(
    new URL('../src/components/PipelineClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(pipeline, /refreshNavCounts/);
  assert.match(pipeline, /needsCountRefreshRef/);
  assert.match(pipeline, /result\.status === 'created'/);
  assert.match(pipeline, /void refreshNavCounts\(\)/);
});
