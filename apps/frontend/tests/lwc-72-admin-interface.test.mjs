import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

test('shell exposes Admin navigation only for management-scope holders', async () => {
  const shell = await readFile(new URL('../src/components/Shell.tsx', import.meta.url), 'utf8');

  assert.match(shell, /Shield/);
  assert.match(shell, /user\?\.management_scopes\?\.some/);
  assert.match(shell, /scope === 'auth:admin'/);
  assert.match(shell, /scope === 'lwc:admin'/);
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
    new URL('../src/app/(workspace)/admin/page.tsx', import.meta.url),
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
  assert.match(adminClient, /user\?\.management_scopes\?\.includes\('auth:admin'\)/);
  assert.match(adminClient, /user\?\.management_scopes\?\.includes\('lwc:admin'\)/);
  assert.match(adminClient, /Admin access required/);
  assert.match(adminClient, /Projects/);
  assert.match(adminClient, /Users/);
  assert.match(adminClient, /getAdminProjects/);
  assert.match(adminClient, /getAdminUsers/);
  assert.match(adminClient, /<table/);
  assert.match(adminClient, /Project name|Project/);
  assert.match(adminClient, /Project ID/);
  assert.match(adminClient, /Owner/);
  assert.match(adminClient, /User ID/);
  assert.match(adminClient, /project\.projectId/);
  assert.match(adminClient, /user\.id/);
  assert.match(adminClient, /adminOwnerPrimary|userName/);
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
  assert.match(adminClient, /updateAdminUserManagementScope/);
  assert.match(adminClient, /updateAdminUserStatus/);
  assert.match(adminClient, /deleteAdminUser/);
  assert.match(adminClient, /canDeleteUser=\{isAuthAdmin && isLWCAdmin\}/);
  assert.match(adminClient, /ConfirmActionModal/);
  assert.match(adminClient, /ScopeActionModal/);
  assert.match(adminClient, /RenameProjectModal/);
  assert.match(adminClient, /announcementMarkdown\.trim\(\) === '' && !window\.confirm/);
  assert.match(adminClient, /setNotice/);
  assert.match(adminClient, /await loadProjects\(\)/);
  assert.match(adminClient, /await loadUsers\(\)/);
});
