import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

import { normalizeStatus } from '../src/lib/api.ts';

test('status normalization exposes suggested_queries as suggestedQueries', () => {
  assert.deepEqual(
    normalizeStatus({
      suggested_queries: ['first query', '', 'second query', 42],
    }).suggestedQueries,
    ['first query', 'second query'],
  );

  assert.deepEqual(normalizeStatus({}).suggestedQueries, []);
});

test('home search placeholder comes from suggested queries before the i18n fallback', async () => {
  const homeClient = await readFile(
    new URL('../src/components/HomeClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(homeClient, /const suggestedQueries = status\?\.suggestedQueries \?\? \[\];/);
  assert.match(
    homeClient,
    /placeholder=\{suggestedQueries\[0\] \?\? t\('Demo\.searchPlaceholder'\)\}/,
  );
});

test('home renders suggested query chips that fill the query from status suggestions', async () => {
  const homeClient = await readFile(
    new URL('../src/components/HomeClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(homeClient, /const suggestedQueryChips = suggestedQueries\.slice\(1\);/);
  assert.match(homeClient, /suggestedQueryChips\.map\(\(suggestion\) => \(/);
  assert.match(homeClient, /onClick=\{\(\) => void handleSuggestedQuery\(suggestion\)\}/);
  assert.match(homeClient, /setQuery\(suggestion\)/);
});

test('zh-TW fallback placeholder is neutral and not the lifestyle demo query', async () => {
  const zhTW = JSON.parse(
    await readFile(new URL('../src/messages/zh-TW.json', import.meta.url), 'utf8'),
  );

  assert.equal(zhTW.Demo.searchPlaceholder, '搜尋你的知識庫…');
  assert.notEqual(zhTW.Demo.searchPlaceholder, '新北適合帶小孩放電的地方');
});
