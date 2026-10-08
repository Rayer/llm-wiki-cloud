// Standalone integration check: one real Next/webpack build, then two app
// lifecycles load different runtime files from the same emitted browser bundle.
// Run: node apps/frontend/tests/lwc-318-built-config.mjs
import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { chmodSync, cpSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import vm from 'node:vm';

const frontend = new URL('..', import.meta.url).pathname;
const configURL = 'https://config.build.example/frontend-config.json';
const root = mkdtempSync(join(tmpdir(), 'lwc-318-built-'));
mkdirSync(join(root, 'src/app'), { recursive: true });
mkdirSync(join(root, 'src/lib'));
for (const name of ['auth-core.ts', 'public-build-config.ts', 'runtime-config.ts', 'google-auth.ts', 'api.ts', 'raw-file-name.ts', 'source-annotation.ts']) {
  cpSync(join(frontend, 'src/lib', name), join(root, 'src/lib', name));
}
cpSync(join(frontend, 'src/app/build-config.json'), join(root, 'src/app/build-config.json'), { recursive: true });
symlinkSync(join(frontend, 'node_modules'), join(root, 'node_modules'));
writeFileSync(join(root, 'package.json'), JSON.stringify({ private: true, scripts: { build: 'next build --webpack' } }));
writeFileSync(join(root, 'tsconfig.json'), JSON.stringify({ compilerOptions: { allowImportingTsExtensions: true, noEmit: true, jsx: 'preserve', moduleResolution: 'bundler' }, exclude: ['node_modules'] }));
writeFileSync(join(root, 'src/app/layout.tsx'), 'export default function Layout({children}: {children: React.ReactNode}) { return <html><body>{children}</body></html> }');
writeFileSync(join(root, 'src/app/page.tsx'), `'use client';
import { loadRuntimeConfig } from '../lib/runtime-config';
import { startGoogleLogin, readGoogleCompletionResult } from '../lib/google-auth';
import { apiFetch, configureApiAuth, getBuildInfo, getPublicConfig } from '../lib/api';
export default function Page() {
  configureApiAuth({ getAccessToken: () => 'fixture-token', refreshAccessToken: async () => null, onUnauthorized: () => undefined });
  Object.assign(globalThis, { __lwc318: { loadRuntimeConfig, startGoogleLogin, readGoogleCompletionResult, apiFetch, getBuildInfo, getPublicConfig } });
  return null;
}`);
const env = {
  ...process.env,
  NEXT_TELEMETRY_DISABLED: '1',
  NEXT_PUBLIC_CONFIG_URL: configURL,
};
execFileSync(process.execPath, [join(frontend, 'node_modules/next/dist/bin/next'), 'build', '--webpack'], { cwd: root, env, stdio: 'inherit' });
const manifest = JSON.parse(readFileSync(join(root, '.next/server/app/build-config.json.body'), 'utf8'));
const prerender = JSON.parse(readFileSync(join(root, '.next/prerender-manifest.json'), 'utf8'));
assert.equal(prerender.routes['/build-config.json'].initialRevalidateSeconds, false);
assert.deepEqual(manifest, { schema_version: 1, config_url: configURL });

const chunks = join(root, '.next/static/chunks');
const chunkFiles = readdirSync(chunks, { recursive: true }).filter((name) => name.endsWith('.js') && !name.includes('webpack-'));
const configs = [
  { schema_version: 1, api_url: 'https://api-a.runtime.example.test', auth_url: 'https://auth-a.runtime.example.test' },
  { schema_version: 1, api_url: 'https://api-b.runtime.example.test', auth_url: 'https://auth-b.runtime.example.test' },
];
for (const runtimeConfig of configs) {
  const requests = [];
  const navigations = [];
  const context = vm.createContext({
    self: {},
    AbortController,
    URL,
    setTimeout,
    clearTimeout,
    window: {
      location: { href: 'https://frontend.example.test/', assign: (url) => navigations.push(url) },
      localStorage: { getItem: () => 'project-1' },
    },
    fetch: async (input, init) => {
      const url = String(input);
      requests.push({ url, init });
      if (url === configURL) return { ok: true, status: 200, json: async () => runtimeConfig };
      if (url.endsWith('/api/v1/public/version')) return { ok: true, status: 200, json: async () => ({
        product_version: '1.2.3', commit: 'fixture', branch: 'main', tag: '',
        image_tag: 'fixture', service: 'bff', revision: 'bff-fixture',
      }) };
      if (url.endsWith('/api/v1/public/config')) return { ok: true, status: 200, json: async () => ({}) };
      if (url.endsWith('/api/v1/auth/google/complete')) return { ok: true, status: 200, json: async () => ({ status: 'cancelled' }) };
      if (url.endsWith('/api/v1/status')) return { ok: true, status: 200, json: async () => ({}) };
      return { ok: true, status: 200, json: async () => ({}) };
    },
  });
  for (const name of chunkFiles) vm.runInContext(readFileSync(join(chunks, name), 'utf8'), context);
  const factories = Object.assign({}, ...context.self.webpackChunk_N_E.map((chunk) => chunk[1]));
  const cache = {};
  function require(id) {
    if (cache[id]) return cache[id].exports;
    const compiledModule = cache[id] = { exports: {} };
    factories[id](compiledModule, compiledModule.exports, require);
    return compiledModule.exports;
  }
  require.d = (exports, getters) => {
    for (const [key, get] of Object.entries(getters)) Object.defineProperty(exports, key, { get });
  };
  require.r = (exports) => Object.defineProperty(exports, '__esModule', { value: true });
  require.o = (object, key) => Object.hasOwn(object, key);
  require.g = context;
  const page = Object.keys(factories).find((id) => factories[id].toString().includes('__lwc318'));
  assert.ok(page, 'compiled client probe must exist');
  require(page).default();
  await context.__lwc318.loadRuntimeConfig();
  await context.__lwc318.apiFetch('/api/v1/status', { requireProject: false });
  await context.__lwc318.getBuildInfo();
  await context.__lwc318.getPublicConfig();
  context.__lwc318.startGoogleLogin();
  await context.__lwc318.readGoogleCompletionResult();

  const configRequest = requests.find(({ url }) => url === configURL);
  assert.equal(configRequest.init.method, 'GET');
  assert.equal(configRequest.init.credentials, 'omit');
  assert.equal(configRequest.init.cache, 'no-store');
  assert.equal(configRequest.init.headers?.Authorization, undefined);
  assert.equal(configRequest.init.signal.aborted, false);
  assert.ok(requests.some(({ url }) => url === `${runtimeConfig.api_url}/api/v1/status`));
  assert.ok(requests.some(({ url }) => url === `${runtimeConfig.api_url}/api/v1/public/version`));
  assert.ok(requests.some(({ url }) => url === `${runtimeConfig.api_url}/api/v1/public/config`));
  assert.ok(requests.some(({ url }) => url === `${runtimeConfig.auth_url}/api/v1/auth/google/complete`));
  assert.ok(navigations.includes(`${runtimeConfig.auth_url}/api/v1/auth/google/login/start`));

  // The deployment receipt proves the reader schema and fixed bootstrap file URL only.
  mkdirSync(join(root, 'bin'), { recursive: true });
  cpSync(join(frontend, 'tests/fixtures/lwc-306-fake-curl'), join(root, 'bin/curl'));
  chmodSync(join(root, 'bin/curl'), 0o755);
  cpSync(join(root, '.next/server/app/build-config.json.body'), join(root, 'build-config.json'));
  writeFileSync(join(root, 'scenario'), 'success');
  writeFileSync(join(root, 'plan.json'), JSON.stringify({ normalized: { frontend: { config_url: configURL } } }));
  const verification = spawnSync('bash', ['-c', 'source "$ROOT/deploy/components/frontend.sh" help >/dev/null; frontend_verify_build_config dpl_frontendnew'], {
    env: { ...env, VERCEL_TOKEN: 'fixture-token', VERCEL_TEAM_ID: 'team_frontendtest', VERCEL_PROJECT_ID: 'prj_frontendtest', ROOT: join(frontend, '../..'), FIXTURE_ROOT: root, PLAN_PATH: join(root, 'plan.json'), PATH: `${root}/bin:${process.env.PATH}` }, encoding: 'utf8',
  });
  assert.equal(verification.status, 0, verification.stderr);
  writeFileSync(join(root, 'plan.json'), JSON.stringify({ normalized: { frontend: { config_url: 'https://wrong-config.runtime.example.test/frontend-config.json' } } }));
  const wrong = spawnSync('bash', ['-c', 'source "$ROOT/deploy/components/frontend.sh" help >/dev/null; frontend_verify_build_config dpl_frontendnew'], {
    env: { ...env, VERCEL_TOKEN: 'fixture-token', VERCEL_TEAM_ID: 'team_frontendtest', VERCEL_PROJECT_ID: 'prj_frontendtest', ROOT: join(frontend, '../..'), FIXTURE_ROOT: root, PLAN_PATH: join(root, 'plan.json'), PATH: `${root}/bin:${process.env.PATH}` }, encoding: 'utf8',
  });
  assert.equal(wrong.status, 1, wrong.stderr);
  console.log(JSON.stringify({ runtime_config: runtimeConfig, request_urls: requests.map(({ url }) => url), receipt_verifier_exit: verification.status, wrong_receipt_exit: wrong.status }));
}
