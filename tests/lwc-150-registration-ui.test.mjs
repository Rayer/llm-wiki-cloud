import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

import {
  clearPublicConfigCache,
  configureApiAuth,
  getAdminSettings,
  getPublicConfig,
  updateAdminSettings,
} from '../src/lib/api.ts';

const readJson = async (path) => JSON.parse(await readFile(path, 'utf8'));

test('getPublicConfig fetches public config without auth or project header', async () => {
  const originalFetch = globalThis.fetch;
  let requestedUrl = '';
  let requestedInit;
  globalThis.fetch = async (url, init) => {
    requestedUrl = String(url);
    requestedInit = init;
    return Response.json({ registration_enabled: false });
  };

  try {
    clearPublicConfigCache();
    const config = await getPublicConfig({ refresh: true });

    assert.equal(
      requestedUrl,
      'https://llm-wiki-bff-dev-580854833715.asia-east1.run.app/api/v1/public/config',
    );
    assert.equal(requestedInit?.headers?.Authorization, undefined);
    assert.equal(requestedInit?.headers?.['X-Project-ID'], undefined);
    assert.deepEqual(config, { registration_enabled: false });
  } finally {
    globalThis.fetch = originalFetch;
    clearPublicConfigCache();
  }
});

test('getPublicConfig caches responses until refresh is requested', async () => {
  const originalFetch = globalThis.fetch;
  let fetchCount = 0;
  globalThis.fetch = async () => {
    fetchCount += 1;
    return Response.json({ registration_enabled: true });
  };

  try {
    clearPublicConfigCache();
    await getPublicConfig();
    await getPublicConfig();
    await getPublicConfig({ refresh: true });

    assert.equal(fetchCount, 2);
  } finally {
    globalThis.fetch = originalFetch;
    clearPublicConfigCache();
  }
});

test('getAdminSettings and updateAdminSettings use admin settings endpoint without project header', async () => {
  configureApiAuthForAdmin();

  const originalFetch = globalThis.fetch;
  const calls = [];
  globalThis.fetch = async (url, init) => {
    calls.push({ url: String(url), init });
    if (String(url).endsWith('/api/v1/admin/settings') && init?.method === 'PATCH') {
      return Response.json({ registration_enabled: false });
    }
    return Response.json({ registration_enabled: true });
  };

  try {
    const settings = await getAdminSettings();
    const updated = await updateAdminSettings({ registration_enabled: false });

    assert.deepEqual(settings, { registration_enabled: true });
    assert.deepEqual(updated, { registration_enabled: false });
    assert.deepEqual(
      calls.map((call) => [call.url, call.init?.method, call.init?.headers?.['X-Project-ID']]),
      [
        [
          'https://llm-wiki-bff-dev-580854833715.asia-east1.run.app/api/v1/admin/settings',
          undefined,
          undefined,
        ],
        [
          'https://llm-wiki-bff-dev-580854833715.asia-east1.run.app/api/v1/admin/settings',
          'PATCH',
          undefined,
        ],
      ],
    );
    assert.equal(calls[1].init.body, JSON.stringify({ registration_enabled: false }));
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('LoginModal loads public config and hides signup when registration is disabled', async () => {
  const loginModal = await readFile(
    new URL('../src/components/LoginModal.tsx', import.meta.url),
    'utf8',
  );

  assert.match(loginModal, /getPublicConfig/);
  assert.match(loginModal, /registration_enabled/);
  assert.match(loginModal, /registrationEnabled/);
  assert.match(loginModal, /registrationEnabled === true \? \([\s\S]*setRegisterOpen\(true\)/);
  assert.match(loginModal, /registrationEnabled === true && registerOpen/);
});

test('LoginModal fails safe by hiding signup when public config fetch fails', async () => {
  const loginModal = await readFile(
    new URL('../src/components/LoginModal.tsx', import.meta.url),
    'utf8',
  );

  assert.match(loginModal, /\.catch\(/);
  assert.match(loginModal, /setRegistrationEnabled\(false\)/);
});

test('RegisterModal shows localized disabled message for registration-disabled errors', async () => {
  const registerModal = await readFile(
    new URL('../src/components/RegisterModal.tsx', import.meta.url),
    'utf8',
  );

  assert.match(registerModal, /RegistrationDisabledError/);
  assert.match(registerModal, /t\('Register\.disabled'\)/);
});

test('AdminClient wires registration toggle through admin settings API', async () => {
  const adminClient = await readFile(
    new URL('../src/components/AdminClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(adminClient, /getAdminSettings/);
  assert.match(adminClient, /updateAdminSettings/);
  assert.match(adminClient, /registration_enabled/);
  assert.match(adminClient, /registrationEnabled/);
  assert.match(adminClient, /type="checkbox"/);
  assert.match(adminClient, /setRegistrationEnabled\(!registrationEnabled\)/);
  assert.match(adminClient, /clearPublicConfigCache/);
});

test('registration i18n keys exist in English and Traditional Chinese catalogs', async () => {
  const [english, traditionalChinese] = await Promise.all([
    readJson(new URL('../src/messages/en.json', import.meta.url)),
    readJson(new URL('../src/messages/zh-TW.json', import.meta.url)),
  ]);

  assert.equal(english.Register.disabled, 'New user registration is currently disabled.');
  assert.equal(traditionalChinese.Register.disabled, '目前不開放新使用者註冊。');
  assert.equal(english.Admin.registrationEnabled, 'Allow new user registration');
  assert.equal(traditionalChinese.Admin.registrationEnabled, '允許新使用者註冊');
});

function configureApiAuthForAdmin() {
  configureApiAuth({
    getAccessToken: () => 'jwt-token',
    refreshAccessToken: async () => null,
    onUnauthorized: () => undefined,
  });
  globalThis.window = {
    localStorage: {
      getItem: () => {
        throw new Error('admin request must not read selected project');
      },
    },
  };
}