import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

test('LWC-68: Shell hides create-project control in demo sessions and exposes it for normal sessions', async () => {
  const shell = await readFile(new URL('../src/components/Shell.tsx', import.meta.url), 'utf8');

  assert.match(shell, /onClick=\{openNewProject\}[\s\S]*t\('Shell\.newProject'\)[\s\S]*\) : null/);

  assert.match(shell, /!isDemoSession/);

  assert.doesNotMatch(
    shell,
    /const handleNewProjectClick = \(\) => \{[\s\S]*Demo\.restricted/,
  );

  const createClickInstances = (shell.match(/onClick=\{openNewProject\}/g) || []).length;
  assert.equal(createClickInstances, 1);

  assert.match(
    shell,
    /const \{[\s\S]*\s*isDemoSession,[\s\S]*\s*openNewProject,[\s\S]*\}/,
  );
});
