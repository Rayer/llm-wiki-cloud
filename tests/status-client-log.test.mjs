import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

test('status client renders pipeline log in a scrollable expandable pre block', async () => {
  const statusClient = await readFile(
    new URL('../src/components/StatusClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(statusClient, /getStatus/);
  assert.match(statusClient, /getPipelineLog/);
  assert.doesNotMatch(statusClient, /getPipelineStatus/);
  assert.match(statusClient, /LOG_PREVIEW_BYTES\s*=\s*10 \* 1024/);
  assert.match(statusClient, /LOG_PREVIEW_LINES\s*=\s*50/);
  assert.match(statusClient, /slice\(-LOG_PREVIEW_LINES\)/);
  assert.match(statusClient, /overflow-y-auto/);
  assert.match(statusClient, /<pre/);
  assert.match(statusClient, /showFullLog/);
});
