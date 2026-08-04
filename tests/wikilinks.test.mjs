import assert from 'node:assert/strict';
import test from 'node:test';

import { resolveWikilink } from '../src/lib/wikilinks.ts';

test('resolveWikilink uses canonical collection target while displaying alias', () => {
  assert.deepEqual(
    resolveWikilink('concepts/a3f7b2c01d9d-鋼鐵機甲戰隊|鋼鐵機甲戰隊', 'concepts'),
    {
      href: '/concepts/a3f7b2c01d9d-%E9%8B%BC%E9%90%B5%E6%A9%9F%E7%94%B2%E6%88%B0%E9%9A%8A',
      label: '鋼鐵機甲戰隊',
    },
  );
});

test('resolveWikilink falls back to section routing for plain slug links', () => {
  assert.deepEqual(resolveWikilink('台中', 'concepts'), {
    href: '/concepts/%E5%8F%B0%E4%B8%AD',
    label: '台中',
  });
  assert.deepEqual(resolveWikilink('新聞來源', 'sources'), {
    href: '/sources/%E6%96%B0%E8%81%9E%E4%BE%86%E6%BA%90',
    label: '新聞來源',
  });
});

test('resolveWikilink matches canonical id-slug concept targets against plain slugs', () => {
  const existing = new Set(['local-development']);
  const live = resolveWikilink(
    'concepts/a1b2c3d4e5f6-local-development|local-development',
    'concepts',
    existing,
  );
  assert.equal(live.dead, false);
  assert.match(live.href, /^\/concepts\//);
});

test('resolveWikilink recognizes released Synto ULID canonical targets', () => {
  const ulid = '01JAZ5N7Y3K8M2Q4R6T9VWXABC';
  const existing = new Set(['local-development']);
  assert.deepEqual(
    resolveWikilink(`concepts/${ulid}-local-development#setup`, 'concepts', existing),
    {
      href: `/concepts/${ulid}-local-development#setup`,
      label: 'local-development',
      dead: false,
    },
  );
  assert.deepEqual(
    resolveWikilink(`concepts/${ulid}-local-development|Local setup`, 'sources', existing),
    {
      href: `/concepts/${ulid}-local-development`,
      label: 'Local setup',
      dead: false,
    },
  );
});

test('resolveWikilink does not strip invalid ULID-like prefixes', () => {
  for (const prefix of [
    '01jaz5n7y3k8m2q4r6t9vwxabc',
    '81JAZ5N7Y3K8M2Q4R6T9VWXABC',
    '01JAZ5N7Y3K8M2Q4R6T9VWXABI',
    '01JAZ5N7Y3K8M2Q4R6T9VWXAB',
    '01JAZ5N7Y3K8M2Q4R6T9VWXABCD',
  ]) {
    const target = `${prefix}-alpha`;
    assert.equal(resolveWikilink(`concepts/${target}`, 'concepts').label, target, prefix);
  }
});

test('resolveWikilink marks missing concept slugs as dead when existingSlugs is provided', () => {
  const existing = new Set(['Mo-Mo-Paradise']);

  const live = resolveWikilink('Mo-Mo-Paradise', 'concepts', existing);
  assert.equal(live.href, '/concepts/Mo-Mo-Paradise');
  assert.equal(live.label, 'Mo-Mo-Paradise');
  assert.notEqual(live.dead, true);
  assert.deepEqual(resolveWikilink('有余 YoYu bakery&kitchen', 'concepts', existing), {
    href: null,
    label: '有余 YoYu bakery&kitchen',
    dead: true,
  });
});

test('resolveWikilink keeps links live when existingSlugs is omitted', () => {
  assert.deepEqual(resolveWikilink('missing-concept', 'concepts'), {
    href: '/concepts/missing-concept',
    label: 'missing-concept',
  });
});
