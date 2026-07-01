import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

test('list client cache is scoped to the current project', async () => {
  const listClient = await readFile(
    new URL('../src/components/ListClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(listClient, /import \{ useWorkspace \} from '\.\/WorkspaceProvider';/);
  assert.match(listClient, /const \{\s*currentProject\s*\} = useWorkspace\(\);/s);
  assert.match(
    listClient,
    /const cacheKey = `\$\{currentProject\?\.id \?\? 'no-project'\}:\$\{basePath\}`;/,
  );
  assert.match(listClient, /clientCache\.get\(cacheKey\)/);
  assert.match(listClient, /clientCache\.has\(cacheKey\)/);
  assert.match(listClient, /clientCache\.set\(cacheKey, data\)/);
  assert.match(listClient, /\}, \[[^\]]*currentProject[^\]]*\]\);/);
});
