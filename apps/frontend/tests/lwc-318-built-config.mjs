// Standalone integration check: real Next/webpack output, no fonts or network.
// Run: node apps/frontend/tests/lwc-318-built-config.mjs
import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { chmodSync, cpSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import vm from 'node:vm';

const frontend = new URL('..', import.meta.url).pathname;
const api = 'https://bff.build.example';
for (const override of [undefined, 'https://auth.example', 'https://auth-dev.rayer.idv.tw']) {
  const root = mkdtempSync(join(tmpdir(), 'lwc-318-built-'));
  mkdirSync(join(root, 'src/app'), { recursive: true });
  mkdirSync(join(root, 'src/lib'));
  for (const name of ['auth-core.ts', 'public-build-config.ts', 'google-auth.ts', 'api.ts', 'raw-file-name.ts', 'source-annotation.ts']) {
    cpSync(join(frontend, 'src/lib', name), join(root, 'src/lib', name));
  }
  cpSync(join(frontend, 'src/app/build-config.json'), join(root, 'src/app/build-config.json'), { recursive: true });
  symlinkSync(join(frontend, 'node_modules'), join(root, 'node_modules'));
  writeFileSync(join(root, 'package.json'), JSON.stringify({ private: true, scripts: { build: 'next build --webpack' } }));
  writeFileSync(join(root, 'tsconfig.json'), JSON.stringify({ compilerOptions: { allowImportingTsExtensions: true, noEmit: true, jsx: 'preserve', moduleResolution: 'bundler' }, exclude: ['node_modules'] }));
  writeFileSync(join(root, 'src/app/layout.tsx'), 'export default function Layout({children}: {children: React.ReactNode}) { return <html><body>{children}</body></html> }');
  writeFileSync(join(root, 'src/app/page.tsx'), `'use client';
import { AUTH_URL, API_URL } from '../lib/auth-core';
import { startGoogleLogin, readGoogleCompletionResult } from '../lib/google-auth';
import { getPublicConfig } from '../lib/api';
export default function Page() {
  Object.assign(globalThis, { __lwc318: { AUTH_URL, API_URL, startGoogleLogin, readGoogleCompletionResult, getPublicConfig } });
  return null;
}`);
  const env = { ...process.env, NEXT_TELEMETRY_DISABLED: '1', NEXT_PUBLIC_API_URL: api };
  delete env.NEXT_PUBLIC_AUTH_URL;
  if (override !== undefined) env.NEXT_PUBLIC_AUTH_URL = override;
  execFileSync(process.execPath, [join(frontend, 'node_modules/next/dist/bin/next'), 'build', '--webpack'], { cwd: root, env, stdio: 'inherit' });
  const manifest = JSON.parse(readFileSync(join(root, '.next/server/app/build-config.json.body'), 'utf8'));
  const prerender = JSON.parse(readFileSync(join(root, '.next/prerender-manifest.json'), 'utf8'));
  assert.equal(prerender.routes['/build-config.json'].initialRevalidateSeconds, false);
  const expected = { schema_version: 1, api_url: api, auth_url: override ?? 'https://auth.dev.rayer.idv.tw' };
  assert.deepEqual(manifest, expected);

  // Evaluate the emitted browser modules with navigation/fetch stubs. No server
  // source evaluation and no real OAuth request is used for these assertions.
  const destinations = [];
  const context = vm.createContext({
    self: {}, window: { location: { assign: (url) => destinations.push(url) } },
    fetch: async (url) => { destinations.push(url); return { ok: true, json: async () => ({ status: 'cancelled' }) }; },
  });
  const chunks = join(root, '.next/static/chunks');
  for (const name of readdirSync(chunks, { recursive: true }).filter((name) => name.endsWith('.js') && !name.includes('webpack-'))) {
    vm.runInContext(readFileSync(join(chunks, name), 'utf8'), context);
  }
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
  assert.equal(context.__lwc318.AUTH_URL, manifest.auth_url);
  assert.equal(context.__lwc318.API_URL, manifest.api_url);
  context.__lwc318.startGoogleLogin();
  await context.__lwc318.readGoogleCompletionResult();
  await context.__lwc318.getPublicConfig();
  assert.deepEqual(destinations, [`${manifest.auth_url}/api/v1/auth/google/login/start`, `${manifest.auth_url}/api/v1/auth/google/complete`, `${manifest.api_url}/api/v1/public/config`]);

  // Feed the actual emitted body to the active CD verifier, through mocked HTTP.
  // A wrong explicit build must stay wrong, even when the plan wants canonical DEV.
  mkdirSync(join(root, 'bin'));
  cpSync(join(frontend, 'tests/fixtures/lwc-306-fake-curl'), join(root, 'bin/curl'));
  chmodSync(join(root, 'bin/curl'), 0o755);
  cpSync(join(root, '.next/server/app/build-config.json.body'), join(root, 'build-config.json'));
  writeFileSync(join(root, 'scenario'), 'success');
  const wrong = override === 'https://auth-dev.rayer.idv.tw';
  writeFileSync(join(root, 'plan.json'), JSON.stringify({ normalized: { frontend: { api_url: api, auth_url: wrong ? 'https://auth.dev.rayer.idv.tw' : manifest.auth_url } } }));
  const verification = spawnSync('bash', ['-c', 'source "$ROOT/deploy/components/frontend.sh" help >/dev/null; frontend_verify_build_config dpl_frontendnew'], {
    env: { ...env, VERCEL_TOKEN: 'fixture-token', VERCEL_TEAM_ID: 'team_frontendtest', VERCEL_PROJECT_ID: 'prj_frontendtest', ROOT: join(frontend, '../..'), FIXTURE_ROOT: root, PLAN_PATH: join(root, 'plan.json'), PATH: `${root}/bin:${process.env.PATH}` }, encoding: 'utf8',
  });
  assert.equal(verification.status, wrong ? 1 : 0, verification.stderr);
  console.log(JSON.stringify({ fixture: root, observed: manifest, browser_origins_verified: true, cd_verifier_exit: verification.status }));
}
