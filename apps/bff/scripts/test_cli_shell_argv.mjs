import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createCLICommands } from '../../frontend/src/lib/cli-command.ts';

const repositoryRoot = resolve(fileURLToPath(new URL('../../../', import.meta.url)));
const bffRoot = join(repositoryRoot, 'apps', 'bff');
const temporaryDirectory = mkdtempSync(join(tmpdir(), 'lwc-cli-argv-'));
const windows = process.platform === 'win32';
const binaryName = windows ? 'lwc-sync.exe' : 'lwc-sync';
const root = join(temporaryDirectory, "Root with apostrophe's 東京");
const authOrigin = 'https://auth-origin.example.invalid';
const projectId = 'project-fixture-id';

try {
  const binaryPath = join(temporaryDirectory, binaryName);
  const build = spawnSync('go', ['build', '-o', binaryPath, './cmd/argvcapture'], { cwd: bffRoot, encoding: 'utf8' });
  if (build.status !== 0) throw new Error(`argv fixture build failed: ${build.stderr}`);

  const shell = windows ? 'powershell' : 'posix';
  const commands = createCLICommands({ shell, root, authOrigin, projectId });
  const execution = windows
    ? spawnSync('pwsh', ['-NoLogo', '-NoProfile', '-NonInteractive', '-Command', commands.init], { cwd: temporaryDirectory, encoding: 'utf8' })
    : spawnSync('/bin/bash', ['-c', commands.init], { cwd: temporaryDirectory, encoding: 'utf8' });
  if (execution.error?.code === 'ENOENT' && windows) {
    console.log('NOTRUN: PowerShell 7 (pwsh) is unavailable on this runner.');
    process.exitCode = 77;
  } else {
    assert.equal(execution.status, 0, execution.stderr || execution.error?.message);
    assert.deepEqual(JSON.parse(execution.stdout.trim()), ['init', '--vault', root, '--host', authOrigin, '--project-id', projectId]);
    console.log(`PASS: ${shell} executed the generated command with the expected argv.`);
  }
} finally {
  rmSync(temporaryDirectory, { recursive: true, force: true });
}
