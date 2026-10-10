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

test('source-count answer badge is translated with a count in every locale', async () => {
  const [english, traditionalChinese] = await Promise.all([
    readJson(new URL('../src/messages/en.json', import.meta.url)),
    readJson(new URL('../src/messages/zh-TW.json', import.meta.url)),
  ]);

  assert.equal(typeof english.Demo.sourcesCount, 'string');
  assert.match(english.Demo.sourcesCount, /\{count\}/);
  assert.equal(typeof traditionalChinese.Demo.sourcesCount, 'string');
  assert.match(traditionalChinese.Demo.sourcesCount, /\{count\}/);
});

test('requested frontend components read their copy from the locale hook', async () => {
  const [loginModal, comingSoonModal, shell, homeClient, i18n] = await Promise.all([
    readFile(new URL('../src/components/LoginModal.tsx', import.meta.url), 'utf8'),
    readFile(new URL('../src/components/ComingSoonModal.tsx', import.meta.url), 'utf8'),
    readFile(new URL('../src/components/Shell.tsx', import.meta.url), 'utf8'),
    readFile(new URL('../src/components/HomeClient.tsx', import.meta.url), 'utf8'),
    readFile(new URL('../src/lib/i18n.ts', import.meta.url), 'utf8'),
  ]);

  assert.match(i18n, /export function useT/);
  assert.match(i18n, /export const useLocale = useT/);
  assert.match(loginModal, /useLocale\(\)/);
  assert.match(loginModal, /t\('Login\.brand'\)/);
  assert.match(loginModal, /t\('Login\.signUp'\)/);
  assert.match(comingSoonModal, /useLocale\(\)/);
  assert.match(comingSoonModal, /t\('ComingSoon\.title'\)/);
  assert.match(shell, /useT\(\)/);
  assert.match(shell, /t\('Shell\.search'\)/);
  assert.match(shell, /t\('Shell\.newProject'\)/);
  assert.match(homeClient, /useT\(\)/);
  assert.match(homeClient, /t\('Demo\.heading'\)/);
  assert.match(homeClient, /t\('Demo\.searchPlaceholder'\)/);
  assert.match(homeClient, /t\(`Demo\.\$\{item\}`\)/);
  assert.match(homeClient, /t\('Demo\.search'\)/);
});

test('account settings copy covers both locales and keeps backend messages separate', async () => {
  const [english, traditionalChinese, shell, modal] = await Promise.all([
    readJson(new URL('../src/messages/en.json', import.meta.url)),
    readJson(new URL('../src/messages/zh-TW.json', import.meta.url)),
    readFile(new URL('../src/components/Shell.tsx', import.meta.url), 'utf8'),
    readFile(new URL('../src/components/AccountSettingsModal.tsx', import.meta.url), 'utf8'),
  ]);
  const keys = [
    'title',
    'close',
    'cliSessionStatusActive',
    'cliSessionStatusRevoked',
    'cliSessionStatusExpired',
    'cliSessionsLoadError',
    'syncBindingsLoadError',
    'googleLinkError',
    'cliSessionRevokeError',
    'syncBindingRevokeError',
    'syncBindingReauthorizeError',
  ];

  for (const key of keys) {
    assert.equal(typeof english.AccountSettings[key], 'string', `English AccountSettings.${key} is missing`);
    assert.equal(typeof traditionalChinese.AccountSettings[key], 'string', `Traditional Chinese AccountSettings.${key} is missing`);
    assert.notEqual(english.AccountSettings[key], `AccountSettings.${key}`);
    assert.notEqual(traditionalChinese.AccountSettings[key], `AccountSettings.${key}`);
  }

  assert.match(shell, /t\('AccountSettings\.title'\)/);
  assert.match(modal, /t\('AccountSettings\.close'\)/);
  for (const key of keys.slice(2)) assert.match(modal, new RegExp(`t\\('AccountSettings\\.${key}'\\)`));
  assert.match(modal, /requestError instanceof Error \? requestError\.message/);
  assert.match(modal, /: session\.status/);
  assert.match(modal, /: binding\.status/);
});
