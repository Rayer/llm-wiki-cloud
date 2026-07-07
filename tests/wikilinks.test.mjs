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
