import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

test('sidebar keeps the user panel fixed while nav content scrolls', async () => {
  const shell = await readFile(
    new URL('../src/components/Shell.tsx', import.meta.url),
    'utf8',
  );

  const asideClass = shell.match(/<aside[\s\S]*?className=\{`([^`]+)\$\{/);
  assert.ok(asideClass, 'expected sidebar aside to use a template className');
  for (const className of ['flex', 'h-dvh', 'w-64', 'shrink-0', 'flex-col', 'lg:sticky', 'lg:top-0']) {
    assert.match(asideClass[1], new RegExp(className.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')));
  }
  assert.doesNotMatch(asideClass[1], /\bh-full\b/);
  assert.doesNotMatch(asideClass[1], /\boverflow-y-auto\b/);
  assert.doesNotMatch(asideClass[1], /\blg:static\b/);

  assert.match(shell, /className="min-h-dvh text-zinc-100 lg:flex lg:items-stretch"/);
  assert.match(shell, /<div className="flex-1 overflow-y-auto">[\s\S]*?<nav aria-label=\{t\('Shell\.navigation'\)\}/);
  assert.match(shell, /<div className="shrink-0 border-t border-white\/8 px-3 py-3">/);
});
