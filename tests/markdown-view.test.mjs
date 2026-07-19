import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

test('MarkdownView renders internal wikilinks with guarded navigation', async () => {
  const markdownView = await readFile(
    new URL('../src/components/MarkdownView.tsx', import.meta.url),
    'utf8',
  );

  assert.match(markdownView, /import \{ NavigationLink \} from '.\/NavigationBlocker';/);
  assert.match(markdownView, /<NavigationLink key=\{index\} href=\{resolved\.href!\}/);
  assert.match(markdownView, /text-red-400 cursor-pointer underline/);
  assert.match(markdownView, /此 concept 尚不存在/);
});

// LWC-107: **[[wikilink]]** must re-parse inner content so the link is not plain bold text
test('MarkdownView re-parses inline markup inside bold markers', async () => {
  const markdownView = await readFile(
    new URL('../src/components/MarkdownView.tsx', import.meta.url),
    'utf8',
  );

  // Bold must recurse into renderInline (covers **[[path|label]]** and nested inline)
  assert.match(
    markdownView,
    /part\.startsWith\('\*\*'\) && part\.endsWith\('\*\*'\)[\s\S]*?<strong key=\{index\}>[\s\S]*?\{renderInline\(part\.slice\(2, -2\)/,
  );
  // Must not dump raw bold body as a plain string child
  assert.doesNotMatch(
    markdownView,
    /<strong key=\{index\}>\{part\.slice\(2, -2\)\}<\/strong>/,
  );
});

// LWC-134: wikilink + parenthetical annotation must not absorb trailing ) into label
test('MarkdownView normalizes wikilink annotations before inline split', async () => {
  const markdownView = await readFile(
    new URL('../src/components/MarkdownView.tsx', import.meta.url),
    'utf8',
  );

  assert.match(markdownView, /normalizeWikilinkAnnotations/);
  assert.match(markdownView, /parseWikilinkToken/);
  assert.match(markdownView, /INLINE_TOKEN_REGEX/);
});
