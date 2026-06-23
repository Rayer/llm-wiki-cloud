import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

const readJson = async (path) => JSON.parse(await readFile(path, 'utf8'));

test('English and Traditional Chinese catalogs expose the same translation keys', async () => {
  const [english, traditionalChinese] = await Promise.all([
    readJson(new URL('../src/messages/en.json', import.meta.url)),
    readJson(new URL('../src/messages/zh-TW.json', import.meta.url)),
  ]);

  assert.deepEqual(Object.keys(traditionalChinese), Object.keys(english));
  for (const section of Object.keys(english)) {
    assert.deepEqual(
      Object.keys(traditionalChinese[section]),
      Object.keys(english[section]),
      `${section} keys differ between locales`,
    );
  }
});

test('requested frontend components read their copy from the useT hook', async () => {
  const [loginModal, comingSoonModal, shell, homeClient, i18n] = await Promise.all([
    readFile(new URL('../src/components/LoginModal.tsx', import.meta.url), 'utf8'),
    readFile(new URL('../src/components/ComingSoonModal.tsx', import.meta.url), 'utf8'),
    readFile(new URL('../src/components/Shell.tsx', import.meta.url), 'utf8'),
    readFile(new URL('../src/components/HomeClient.tsx', import.meta.url), 'utf8'),
    readFile(new URL('../src/lib/i18n.ts', import.meta.url), 'utf8'),
  ]);

  assert.match(i18n, /export function useT/);
  assert.match(loginModal, /useT\(\)/);
  assert.match(loginModal, /t\('Login\.brand'\)/);
  assert.match(loginModal, /t\('Login\.signUp'\)/);
  assert.match(comingSoonModal, /useT\(\)/);
  assert.match(comingSoonModal, /t\('ComingSoon\.title'\)/);
  assert.match(shell, /useT\(\)/);
  assert.match(shell, /t\('Shell\.search'\)/);
  assert.match(shell, /t\('Shell\.demoDisabled'\)/);
  assert.match(homeClient, /useT\(\)/);
  assert.match(homeClient, /t\('Demo\.heading'\)/);
  assert.match(homeClient, /t\('Demo\.searchPlaceholder'\)/);
  assert.match(homeClient, /t\(`Demo\.\$\{item\}`\)/);
  assert.match(homeClient, /t\('Demo\.search'\)/);
});
