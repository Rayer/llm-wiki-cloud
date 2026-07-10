import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

import { primaryRawFileName } from '../src/lib/raw-file-name.ts';

test('primaryRawFileName uses first frontmatter.sources entry and strips raw/', () => {
  assert.equal(
    primaryRawFileName({ sources: ['raw/article.md', 'raw/other.md'] }),
    'article.md',
  );
  assert.equal(primaryRawFileName({ sources: ['note.md'] }), 'note.md');
  assert.equal(primaryRawFileName({ sources: [] }), null);
  assert.equal(primaryRawFileName({}), null);
  assert.equal(primaryRawFileName(undefined), null);
});

test('DetailClient shows prominent raw link only for source entries with sources[0]', async () => {
  const detailClient = await readFile(
    new URL('../src/components/DetailClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(detailClient, /primaryRawFileName/);
  assert.match(detailClient, /entryType === 'source'/);
  assert.match(detailClient, /data-testid="source-raw-file-link"/);
  assert.match(detailClient, /t\('Detail\.rawFile'\)/);
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
