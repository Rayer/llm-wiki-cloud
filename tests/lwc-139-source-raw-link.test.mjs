import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

import { primaryRawFileName } from '../src/lib/raw-file-name.ts';

test('primaryRawFileName prefers sources[0], then source_file (OLW), then slug fallback', () => {
  assert.equal(
    primaryRawFileName({ sources: ['raw/article.md', 'raw/other.md'] }),
    'article.md',
  );
  assert.equal(
    primaryRawFileName({
      source_file: 'raw/夏天消暑看這裡！雙連埤圳頭鴛鴦溪，免健行、水質超清澈的在地玩水點.md',
    }),
    '夏天消暑看這裡！雙連埤圳頭鴛鴦溪，免健行、水質超清澈的在地玩水點.md',
  );
  assert.equal(
    primaryRawFileName({ sources: ['raw/from-array.md'], source_file: 'raw/from-file.md' }),
    'from-array.md',
  );
  assert.equal(primaryRawFileName({ source: 'raw/singular.md' }), 'singular.md');
  assert.equal(primaryRawFileName({}), null);
  assert.equal(primaryRawFileName(undefined), null);
  assert.equal(
    primaryRawFileName({}, { slugFallback: 'my-source' }),
    'my-source.md',
  );
  assert.equal(
    primaryRawFileName({ source_file: 'raw/real.md' }, { slugFallback: 'my-source' }),
    'real.md',
  );
});

test('DetailClient shows prominent raw link for source entries using source_file resolution', async () => {
  const detailClient = await readFile(
    new URL('../src/components/DetailClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(detailClient, /primaryRawFileName/);
  assert.match(detailClient, /entryType === 'source'/);
  assert.match(detailClient, /slugFallback:\s*entry\.slug/);
  assert.match(detailClient, /data-testid="source-raw-file-link"/);
  assert.match(detailClient, /t\('Detail\.rawFile'\)/);
  assert.match(detailClient, /key === 'source_file'/);
  assert.match(
    detailClient,
    /href=\{`\/raw\?file=\$\{encodeURIComponent\(primaryRawFile\)\}`\}/,
  );
});

test('i18n provides Detail.rawFile labels', async () => {
  const zh = JSON.parse(
    await readFile(new URL('../src/messages/zh-TW.json', import.meta.url), 'utf8'),
  );
  const en = JSON.parse(
    await readFile(new URL('../src/messages/en.json', import.meta.url), 'utf8'),
  );

  assert.equal(zh.Detail.rawFile, '原始檔案');
  assert.equal(en.Detail.rawFile, 'Raw file');
});
