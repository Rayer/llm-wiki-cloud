import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

import {
  clearStoredDemoSession,
  DEMO_SESSION_STORAGE_KEY,
  readStoredDemoSession,
  writeStoredDemoSession,
} from '../src/lib/auth-core.ts';

// LWC-135: demo button login restricts create project, upload, and pipeline trigger
test('LWC-135: demo session storage helpers round-trip active flag', () => {
  const data = new Map();
  const storage = {
    getItem: (key) => data.get(key) ?? null,
    setItem: (key, value) => {
      data.set(key, value);
    },
    removeItem: (key) => {
      data.delete(key);
    },
  };

  assert.equal(readStoredDemoSession(storage), false);
  writeStoredDemoSession(storage, true);
  assert.equal(data.get(DEMO_SESSION_STORAGE_KEY), '1');
  assert.equal(readStoredDemoSession(storage), true);
  clearStoredDemoSession(storage);
  assert.equal(readStoredDemoSession(storage), false);
});

test('LWC-135: auth provider exposes demo session state and login paths', async () => {
  const auth = await readFile(new URL('../src/lib/auth.tsx', import.meta.url), 'utf8');

  assert.match(auth, /isDemoSession/);
  assert.match(auth, /loginAsDemo/);
  assert.match(auth, /readStoredDemoSession/);
  assert.match(auth, /writeStoredDemoSession/);
  assert.match(auth, /clearStoredDemoSession/);
  assert.match(auth, /demo:\s*false/);
  assert.match(auth, /demo:\s*true/);
});

test('LWC-135: demo button uses restricted demo login path', async () => {
  const [loginModal, workspaceProvider] = await Promise.all([
    readFile(new URL('../src/components/LoginModal.tsx', import.meta.url), 'utf8'),
    readFile(new URL('../src/components/WorkspaceProvider.tsx', import.meta.url), 'utf8'),
  ]);

  assert.match(loginModal, /signInAsDemo/);
  assert.doesNotMatch(loginModal, /handleDemo[\s\S]*?signIn\('demo@llm-wiki\.dev'/);
  assert.match(workspaceProvider, /signInAsDemo/);
  assert.match(workspaceProvider, /isDemoSession/);
  assert.doesNotMatch(workspaceProvider, /test@example\.com/);
});

test('LWC-135: workspace and shell block create project for demo sessions', async () => {
  const [workspaceProvider, shell] = await Promise.all([
    readFile(new URL('../src/components/WorkspaceProvider.tsx', import.meta.url), 'utf8'),
    readFile(new URL('../src/components/Shell.tsx', import.meta.url), 'utf8'),
  ]);

  assert.match(workspaceProvider, /if \(isDemoSession\) throw new Error/);
  assert.match(workspaceProvider, /if \(isDemoSession\) return/);
  assert.match(shell, /isDemoSession/);
  assert.match(shell, /t\('Demo\.restricted'\)/);
});

test('LWC-135: pipeline client blocks upload and pipeline trigger in demo sessions', async () => {
  const pipelineClient = await readFile(
    new URL('../src/components/PipelineClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(pipelineClient, /isDemoSession/);
  assert.match(pipelineClient, /t\('Demo\.restricted'\)/);
  assert.match(pipelineClient, /enqueueFiles[\s\S]*?isDemoSession/);
  assert.match(pipelineClient, /handleRunPipeline[\s\S]*?isDemoSession/);
});

test('LWC-135: locales expose Demo.restricted copy', async () => {
  const [english, traditionalChinese] = await Promise.all([
    readFile(new URL('../src/messages/en.json', import.meta.url), 'utf8').then(JSON.parse),
    readFile(new URL('../src/messages/zh-TW.json', import.meta.url), 'utf8').then(JSON.parse),
  ]);

  assert.equal(english.Demo.restricted, 'This feature is not available in demo mode');
  assert.equal(traditionalChinese.Demo.restricted, 'Demo 模式不提供此功能');
});

test('LWC-135: demo upload UI is disabled and localStorage backs demo flag', async () => {
  const [pipelineClient, auth] = await Promise.all([
    readFile(new URL('../src/components/PipelineClient.tsx', import.meta.url), 'utf8'),
    readFile(new URL('../src/lib/auth.tsx', import.meta.url), 'utf8'),
  ]);

  assert.match(pipelineClient, /disabled=\{isDemoSession \|\| uploading\}/);
  assert.match(pipelineClient, /htmlFor=\{isDemoSession \? undefined : 'raw-file-upload'\}/);
  assert.match(auth, /writeStoredDemoSession\(\s*typeof window !== 'undefined' \? window\.localStorage/);
  assert.match(auth, /demo@llm-wiki\.dev/);
});
