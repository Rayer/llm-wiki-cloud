import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

// LWC-137: Raw file navigation is driven by the verified source mapping; other source metadata stays text.
test('DetailClient renders frontmatter.sources as text and keeps a prominent Raw reader', async () => {
  const detailClient = await readFile(
    new URL('../src/components/DetailClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(detailClient, /rawFileNameFromSource/);
  assert.match(detailClient, /from '@\/lib\/raw-file-name'/);
  assert.match(detailClient, /function renderFrontmatterValue\(/);
  assert.match(detailClient, /key === 'sources' && Array\.isArray\(value\)/);
  assert.match(detailClient, /{sources\.map\(\(source\) => <li key=\{source\} className="break-all font-mono text-xs">{source}<\/li>\)}/);
  assert.match(detailClient, /function RawFileSection/);
  assert.match(detailClient, /getRawFilePreview\(rawPath\)/);
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
  assert.match(rawClient, /scopedFiles\.find\(\(file\) => file\.name === highlightedFile\)/);
  assert.match(rawClient, /void openRawPreview\(requestedFile\);/);
  assert.match(rawClient, /file\.name === highlightedFile/);
});
