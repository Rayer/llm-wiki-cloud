import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

test('home status reloads when the current project changes', async () => {
  const homeClient = await readFile(
    new URL('../src/components/HomeClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(homeClient, /import \{ WorkspaceProvider, useWorkspace \}|import \{ useWorkspace \}/);
  assert.match(homeClient, /const \{\s*currentProject\s*\} = useWorkspace\(\);/s);
  assert.match(
    homeClient,
    /useEffect\(\(\) => \{\s*getStatus\(\)[\s\S]*?\}, \[currentProject\]\);/,
  );
});

test('shell renders projects with a custom project selector', async () => {
  const shell = await readFile(
    new URL('../src/components/Shell.tsx', import.meta.url),
    'utf8',
  );

  assert.match(shell, /import \{ ProjectSelect \} from '\.\/ui\/ProjectSelect';/);
  assert.match(
    shell,
    /<ProjectSelect[\s\S]*value=\{currentProject\?\.id \?\? ''\}[\s\S]*onChange=\{\(projectId\) => selectProject\(projectId\)\}/,
  );
  assert.match(shell, /projects=\{projects\}/);
  assert.doesNotMatch(shell, /<select[\s\S]*value=\{currentProject\?\.id/);
});

test('shell footer shows user and project ids above the user account block', async () => {
  const shell = await readFile(
    new URL('../src/components/Shell.tsx', import.meta.url),
    'utf8',
  );

  assert.match(shell, /import \{ useAuth \} from '@\/lib\/auth';/);
  assert.match(shell, /const \{\s*user\s*\} = useAuth\(\);/);
  assert.match(
    shell,
    /<div className="mt-2 border-t border-white\/10 pt-2">[\s\S]*?<div className="mt-3 flex items-center gap-3">/,
  );
  assert.match(
    shell,
    /<p className="font-mono text-\[10px\] text-zinc-600 truncate">User: \{user\?\.id \?\? '—'\}<\/p>/,
  );
  assert.match(
    shell,
    /<p className="font-mono text-\[10px\] text-zinc-600 truncate">Project: \{currentProject\?\.id \?\? '—'\}<\/p>/,
  );
});