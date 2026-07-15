import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

test('LWC-141: developer IDs are hidden in expanded status details instead of the shell footer', async () => {
  const [shell, statusClient, english, traditionalChinese] = await Promise.all([
    readFile(new URL('../src/components/Shell.tsx', import.meta.url), 'utf8'),
    readFile(new URL('../src/components/StatusClient.tsx', import.meta.url), 'utf8'),
    readFile(new URL('../src/messages/en.json', import.meta.url), 'utf8').then(JSON.parse),
    readFile(new URL('../src/messages/zh-TW.json', import.meta.url), 'utf8').then(JSON.parse),
  ]);

  assert.doesNotMatch(shell, /User:\s*\{user\?\.id \?\? '—'\}/);
  assert.doesNotMatch(shell, /Project:\s*\{currentProject\?\.id \?\? '—'\}/);

  assert.match(statusClient, /import \{ useAuth \} from '@\/lib\/auth';/);
  assert.match(statusClient, /import \{ useWorkspace \} from '\.\/WorkspaceProvider';/);
  assert.match(statusClient, /const \{ user \} = useAuth\(\);/);
  assert.match(statusClient, /const \{ currentProject \} = useWorkspace\(\);/);
  assert.match(statusClient, /\{showRaw \? \([\s\S]*?t\('Status\.userId'\)[\s\S]*?userId \?\? '—'[\s\S]*?t\('Status\.projectId'\)[\s\S]*?projectId \?\? '—'/);
  assert.match(statusClient, /JSON\.stringify\(\{ api: status\?\.raw \?\? null, build: buildInfo \}, null, 2\)/);
  assert.match(statusClient, /const \[showRaw, setShowRaw\] = useState\(false\);/);

  assert.equal(english.Status.userId, 'User ID');
  assert.equal(english.Status.projectId, 'Project ID');
  assert.equal(traditionalChinese.Status.userId, '使用者 ID');
  assert.equal(traditionalChinese.Status.projectId, '專案 ID');
});
