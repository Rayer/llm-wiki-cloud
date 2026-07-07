import assert from 'node:assert/strict';
import test from 'node:test';

import { parseMarkdownImage } from '../src/lib/markdown-images.ts';

test('parseMarkdownImage ignores optional image title when resolving src', () => {
  assert.deepEqual(
    parseMarkdownImage('![Alt text](https://upssmile.com/photo.jpg "Long image title")'),
    {
      alt: 'Alt text',
      src: 'https://upssmile.com/photo.jpg',
    },
  );
});

test('parseMarkdownImage keeps titles with closing parens out of src', () => {
  assert.deepEqual(
    parseMarkdownImage('![Alt](https://upssmile.com/photo.jpg "Cafe (closed)")'),
    {
      alt: 'Alt',
      src: 'https://upssmile.com/photo.jpg',
    },
  );
});

test('parseMarkdownImage parses plain destination images', () => {
  assert.deepEqual(parseMarkdownImage('![Alt](https://upssmile.com/photo.jpg)'), {
    alt: 'Alt',
    src: 'https://upssmile.com/photo.jpg',
  });
});
