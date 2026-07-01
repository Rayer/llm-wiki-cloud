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
