import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

test('shell exposes Admin navigation only for admin users', async () => {
  const shell = await readFile(new URL('../src/components/Shell.tsx', import.meta.url), 'utf8');

  assert.match(shell, /Shield/);
  assert.match(shell, /user\?\.role === 'admin'/);
  assert.match(shell, /href: '\/admin'/);
  assert.match(shell, /label: 'Admin'/);
});

test('shell explicitly allows admin route content without a selected project', async () => {
  const shell = await readFile(new URL('../src/components/Shell.tsx', import.meta.url), 'utf8');

  assert.match(shell, /pathname === '\/admin'/);
  assert.match(shell, /token && isAdminRoute \?/);
  assert.match(shell, /token && isAdminRoute \?[\s\S]*\{children\}/);
});

test('admin route renders AdminClient', async () => {
  const page = await readFile(
    new URL('../src/app/admin/page.tsx', import.meta.url),
    'utf8',
  );

  assert.match(page, /import \{ AdminClient \} from '@\/components\/AdminClient';/);
  assert.match(page, /return <AdminClient \/>;/);
});

test('AdminClient gates access, renders tabs, and loads admin tables', async () => {
  const adminClient = await readFile(
    new URL('../src/components/AdminClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(adminClient, /useAuth/);
  assert.match(adminClient, /user\?\.role !== 'admin'/);
  assert.match(adminClient, /Admin access required/);
  assert.match(adminClient, /Projects/);
  assert.match(adminClient, /Users/);
  assert.match(adminClient, /getAdminProjects/);
  assert.match(adminClient, /getAdminUsers/);
  assert.match(adminClient, /<table/);
  assert.match(adminClient, /Project name/);
  assert.match(adminClient, /Concept count/);
  assert.match(adminClient, /Source count/);
  assert.match(adminClient, /Project count/);
});

test('AdminClient renders the same access-denied surface for backend admin 403s', async () => {
  const adminClient = await readFile(
    new URL('../src/components/AdminClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(adminClient, /const \[adminDenied, setAdminDenied\] = useState\(false\);/);
  assert.match(adminClient, /if \(error instanceof ApiError && error.status === 403\) \{/);
  assert.match(adminClient, /if \(accessDenied \|\| adminDenied\) \{/);
  assert.match(adminClient, /Admin access required/);
});

test('AdminClient wires project and user actions through confirmation modals', async () => {
  const adminClient = await readFile(
    new URL('../src/components/AdminClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(adminClient, /renameAdminProject/);
  assert.match(adminClient, /deleteAdminProject/);
  assert.match(adminClient, /rebuildAdminProjectIndex/);
  assert.match(adminClient, /triggerAdminProjectPipeline/);
  assert.match(adminClient, /updateAdminUserRole/);
  assert.match(adminClient, /deleteAdminUser/);
  assert.match(adminClient, /ConfirmActionModal/);
  assert.match(adminClient, /RoleActionModal/);
  assert.match(adminClient, /RenameProjectModal/);
  assert.doesNotMatch(adminClient, /window\.confirm/);
  assert.match(adminClient, /setNotice/);
  assert.match(adminClient, /await loadProjects\(\)/);
  assert.match(adminClient, /await loadUsers\(\)/);
});
