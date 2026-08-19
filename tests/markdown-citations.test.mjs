import assert from 'node:assert/strict';
import test from 'node:test';
import { getExactRawCitationRange } from '../src/lib/markdown-citations.ts';

function textNode(value, content) {
  return {
    value,
    position: { start: { offset: 0 }, end: { offset: content.length } },
  };
}

test('maps only an exact raw citation token to its source bytes', () => {
  const content = 'See [Concept].';
  const range = getExactRawCitationRange(textNode(content, content), content, 4, 13);

  assert.deepEqual(range, { start: 4, end: 13 });
  assert.equal(content.slice(range.start, range.end), '[Concept]');
});

test('rejects escaped, entity-decoded, and backslash-variant bracket tokens', () => {
  const cases = [
    ['See \\[Concept].', 'See [Concept].', 4, 13],
    ['See &#91;Concept].', 'See [Concept].', 4, 13],
    ['See &#x5B;Concept].', 'See [Concept].', 4, 13],
    ['See &lbrack;Concept].', 'See [Concept].', 4, 13],
    ['See \\\\[Concept].', 'See \\[Concept].', 5, 14],
  ];

  for (const [content, cooked, start, end] of cases) {
    assert.equal(getExactRawCitationRange(textNode(cooked, content), content, start, end), null, content);
  }
});

test('keeps a genuine token mappable beside an ambiguous token', () => {
  const content = 'See \\[Concept] and [Concept].';
  const cooked = 'See [Concept] and [Concept].';
  const start = cooked.indexOf('[Concept]', cooked.indexOf('and'));
  const range = getExactRawCitationRange(textNode(cooked, content), content, start, start + 9);

  assert.deepEqual(range, { start: content.lastIndexOf('[Concept]'), end: content.length - 1 });
});
