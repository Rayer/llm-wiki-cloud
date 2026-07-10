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
  for (const className of ['flex', 'h-full', 'w-64', 'shrink-0', 'flex-col']) {
    assert.match(asideClass[1], new RegExp(`\\b${className}\\b`));
  }
  assert.doesNotMatch(asideClass[1], /\boverflow-y-auto\b/);

  assert.match(shell, /<div className="flex-1 overflow-y-auto">[\s\S]*?<nav aria-label=\{t\('Shell\.navigation'\)\}/);
  assert.match(shell, /<div className="shrink-0 border-t border-white\/8 px-3 py-3">/);
});
