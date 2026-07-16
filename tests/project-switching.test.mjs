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

test('shell footer keeps the user account block without raw user or project ids', async () => {
  const shell = await readFile(
    new URL('../src/components/Shell.tsx', import.meta.url),
    'utf8',
  );

  assert.match(shell, /import \{ useAuth \} from '@\/lib\/auth';/);
  assert.match(shell, /const \{\s*user\s*\} = useAuth\(\);/);
  assert.match(shell, /<div className="shrink-0 border-t border-white\/8 px-3 py-3">[\s\S]*?<div className="mt-3 flex items-center gap-3">/);
  assert.match(shell, /\{user\?\.email \?\? 'User'\}/);
  assert.match(shell, /onClick=\{\(\) => void signOut\(\)\}/);
  assert.doesNotMatch(shell, /User:\s*\{user\?\.id \?\? '—'\}/);
  assert.doesNotMatch(shell, /Project:\s*\{currentProject\?\.id \?\? '—'\}/);
});
