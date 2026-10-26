import assert from 'node:assert/strict';
import { existsSync, readFileSync, statSync } from 'node:fs';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const frontend = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const layout = readFileSync(path.join(frontend, 'src/app/layout.tsx'), 'utf8');
const globalStyles = readFileSync(path.join(frontend, 'src/app/globals.css'), 'utf8');

test('LWC-429 loads pinned local fonts and keeps the existing font variables', () => {
  assert.match(layout, /from "next\/font\/local"/);
  assert.doesNotMatch(layout, /next\/font\/google|fonts\.googleapis\.com|fonts\.gstatic\.com/);
  assert.match(layout, /--font-geist-sans/);
  assert.match(layout, /--font-geist-mono/);
  assert.match(layout, /--font-noto-serif/);
  assert.match(layout, /weight: "200 900"/);
  assert.match(layout, /display: "swap"/);
  assert.doesNotMatch(globalStyles, /fonts\.googleapis\.com|fonts\.gstatic\.com/);

  for (const asset of [
    'Geist-Variable.woff2',
    'GeistMono-Variable.woff2',
    'NotoSerifTC[wght].ttf',
    'OFL-Geist.txt',
    'OFL-NotoSerifTC.txt',
  ]) {
    const file = path.join(frontend, 'src/app/fonts', asset);
    assert.equal(existsSync(file), true, 'font asset is committed beside the layout: ' + asset);
    assert.ok(statSync(file).size > 1000, 'font asset is not an empty placeholder: ' + asset);
  }
});
