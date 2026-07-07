import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

test('MarkdownView renders internal wikilinks with Next Link navigation', async () => {
  const markdownView = await readFile(
    new URL('../src/components/MarkdownView.tsx', import.meta.url),
    'utf8',
  );

  assert.match(markdownView, /import Link from 'next\/link';/);
  assert.match(markdownView, /<Link key=\{index\} href=\{resolved\.href\}/);
});
