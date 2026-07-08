import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

test('home renders query expansion keywords below search results', async () => {
  const homeClient = await readFile(
    new URL('../src/components/HomeClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(homeClient, /const \[expandKeywords, setExpandKeywords\]/);
  assert.match(homeClient, /setExpandKeywords\(response\.expand\?\.keywords \?\? \[\]\)/);
  assert.match(homeClient, /搜尋關鍵字：/);
  assert.match(homeClient, /expandKeywords\.join\('、'\)/);
});
