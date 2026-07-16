import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

test('home first-run UX avoids generic examples and exposes latest concept previews', async () => {
  const homeClient = await readFile(
    new URL('../src/components/HomeClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(homeClient, /getConcepts/);
  assert.doesNotMatch(homeClient, /onClick=\{\(\) => setQuery\(suggestion\)\}/);
  assert.doesNotMatch(homeClient, /'機器學習'|親子景點|知識整理|概念關聯/);
  assert.doesNotMatch(homeClient, /'RAG'/);
  assert.doesNotMatch(homeClient, /'向量資料庫'/);
  assert.match(homeClient, /const \[latestConcepts, setLatestConcepts\] = useState<WikiEntry\[\]>\(\[\]\);/);
  assert.match(homeClient, /getConcepts\(\)[\s\S]*setLatestConcepts\(data\.slice\(0, 4\)\)/);
  assert.match(homeClient, /!searched && latestConcepts\.length > 0/);
  assert.match(homeClient, /t\('Demo\.latestConcepts'\)/);
  assert.match(homeClient, /href=\{conceptHref\(concept\)\}/);
});

test('list empty states guide first-run users by entry type', async () => {
  const listClient = await readFile(
    new URL('../src/components/ListClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(listClient, /useT\(\)/);
  assert.match(listClient, /t\('List\.noConcepts'\)/);
  assert.match(listClient, /t\('List\.noSources'\)/);
  assert.match(listClient, /entryType === 'concept'/);
  assert.match(listClient, /entryType === 'source'/);
  assert.doesNotMatch(listClient, /No \$\{title\.toLowerCase\(\)\} were returned by the API\./);
});

test('shell keeps the current route highlighted in sidebar navigation', async () => {
  const shell = await readFile(
    new URL('../src/components/Shell.tsx', import.meta.url),
    'utf8',
  );

  assert.match(shell, /const active = isActive\(item\.href, item\.exact\);/);
  assert.match(shell, /if \(exact\) return pathname === href;/);
  assert.match(shell, /pathname\.startsWith\(`\$\{href\}\/`\)/);
  assert.match(shell, /active[\s\S]*\? 'bg-white\/8 text-white'/);
  assert.match(shell, /active[\s\S]*bg-emerald-400/);
});
