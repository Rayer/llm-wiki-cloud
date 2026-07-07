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

test('shell renders projects with a dropdown selector', async () => {
  const shell = await readFile(
    new URL('../src/components/Shell.tsx', import.meta.url),
    'utf8',
  );

  assert.match(shell, /<select[\s\S]*value=\{currentProject\?\.id \?\? ''\}[\s\S]*onChange=\{\(event\) => selectProject\(event\.target\.value\)\}/);
  assert.match(shell, /projects\.map\(\(project\) => \(\s*<option[\s\S]*key=\{project\.id\}[\s\S]*value=\{project\.id\}[\s\S]*>\s*\{project\.name\}\s*<\/option>/);
  assert.doesNotMatch(shell, /projects\.map\(\(project\) => \(\s*<button/);
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
    /<div className="mt-2 border-t border-white\/10 pt-2">[\s\S]*?<div className="flex items-center gap-3">/,
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
