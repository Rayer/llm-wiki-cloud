import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

const readJson = async (path) => JSON.parse(await readFile(path, 'utf8'));

test('LWC-157: Shell subtitle is translated in both supported locales', async () => {
  const [english, traditionalChinese] = await Promise.all([
    readJson(new URL('../src/messages/en.json', import.meta.url)),
    readJson(new URL('../src/messages/zh-TW.json', import.meta.url)),
  ]);

  assert.equal(english.Shell.subtitle, 'Explore your knowledge base');
  assert.equal(traditionalChinese.Shell.subtitle, '探索你的知識庫');
});
