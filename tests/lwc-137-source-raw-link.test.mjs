import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

// LWC-137: Source detail frontmatter.sources should link back to raw files.
test('DetailClient renders frontmatter.sources as raw file links', async () => {
  const detailClient = await readFile(
    new URL('../src/components/DetailClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(detailClient, /rawFileNameFromSource/);
  assert.match(detailClient, /from '@\/lib\/raw-file-name'/);
  assert.match(detailClient, /function renderFrontmatterValue\(/);
  assert.match(detailClient, /key === 'sources' && Array\.isArray\(value\)/);
  assert.match(detailClient, /href=\{`\/raw\?file=\$\{encodeURIComponent\(rawFileNameFromSource\(source\)\)\}`\}/);
  assert.match(detailClient, /{source}/);
});

test('RawClient opens and highlights the file named by the raw file query param', async () => {
  const rawClient = await readFile(
    new URL('../src/components/RawClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(rawClient, /import \{ useSearchParams \} from 'next\/navigation';/);
  assert.match(rawClient, /const searchParams = useSearchParams\(\);/);
  assert.match(rawClient, /const highlightedFile = searchParams\.get\('file'\) \?\? '';/);
  assert.match(rawClient, /files\.find\(\(file\) => file\.name === highlightedFile\)/);
  assert.match(rawClient, /void openRawPreview\(requestedFile\);/);
  assert.match(rawClient, /file\.name === highlightedFile/);
});
