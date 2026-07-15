import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

import {
  normalizeWikilinkAnnotations,
  parseWikilinkToken,
  resolveWikilinksInMarkdown,
  splitInlineTokens,
} from '../src/lib/markdown-inline.ts';

// LWC-134: [[wikilink]](annotation) must not absorb trailing ) into link label
test('LWC-134: wikilink label stays clean before parenthetical annotation', () => {
  const text = '...轉換為結構化的 [[來源]](Source) 與 [[概念]](Concept)。';
  const parts = splitInlineTokens(normalizeWikilinkAnnotations(text));
  const wikilinks = parts.map(parseWikilinkToken).filter((label) => label !== null);

  assert.deepEqual(wikilinks, ['來源', '概念']);
  assert.ok(parts.includes('(Source) 與 '));
  assert.ok(parts.includes('(Concept)。'));
});

test('LWC-134: normalizes malformed [[label)]](annotation) before splitting', () => {
  const text = '[[來源)]](Source)';
  const normalized = normalizeWikilinkAnnotations(text);
  const parts = splitInlineTokens(normalized);
  const wikilinkPart = parts.find((part) => part.startsWith('[['));

  assert.equal(normalized, '[[來源]](Source)');
  assert.equal(parseWikilinkToken(wikilinkPart), '來源');
  assert.ok(parts.includes('(Source)'));
});

test('LWC-134: keeps plain wikilinks without annotations unchanged', () => {
  const text = 'see [[台北讀書咖啡廳]] for more';
  const normalized = normalizeWikilinkAnnotations(text);

  assert.equal(normalized, text);
  assert.deepEqual(splitInlineTokens(normalized).map(parseWikilinkToken).filter(Boolean), [
    '台北讀書咖啡廳',
  ]);
});

test('LWC-134: modal wikilink resolver keeps annotation parens out of link text', () => {
  const md = '...轉換為結構化的 [[來源)]](Source) 與 [[概念)]](Concept)。';
  const result = resolveWikilinksInMarkdown(md);

  assert.match(result, /\[來源\]\(\/concepts\//);
  assert.match(result, /\[概念\]\(\/concepts\//);
  assert.doesNotMatch(result, /來源\)/);
  assert.doesNotMatch(result, /概念\)/);
  assert.ok(result.includes('(Source)'));
  assert.ok(result.includes('(Concept)。'));
});

test('LWC-163: modal wikilink resolver follows canonical wikilink routing', () => {
  const result = resolveWikilinksInMarkdown(`## Sources
[[sources/4964eea0ce81-title|Title]]
[[legacy source & title#片段|來源別名]]
[[concepts/a3f7b2c01d9d-鋼鐵機甲戰隊|機甲]]
## Concepts
[[legacy concept#fragment]]`);

  assert.match(result, /\[Title\]\(\/sources\/4964eea0ce81-title\)/);
  assert.doesNotMatch(result, /sources%2F/);
  assert.match(
    result,
    /\[來源別名\]\(\/sources\/legacy%20source%20%26%20title#%E7%89%87%E6%AE%B5\)/,
  );
  assert.match(
    result,
    /\[機甲\]\(\/concepts\/a3f7b2c01d9d-%E9%8B%BC%E9%90%B5%E6%A9%9F%E7%94%B2%E6%88%B0%E9%9A%8A\)/,
  );
  assert.match(result, /\[legacy concept\]\(\/concepts\/legacy%20concept#fragment\)/);
});

test('LWC-134: HomeClient uses shared wikilink resolver with annotation normalization', async () => {
  const homeClient = await readFile(
    new URL('../src/components/HomeClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(homeClient, /resolveWikilinksInMarkdown/);
  assert.doesNotMatch(homeClient, /function resolveWikilinks\(/);
});
