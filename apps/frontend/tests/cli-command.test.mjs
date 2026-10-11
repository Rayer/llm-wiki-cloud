import assert from 'node:assert/strict';
import test from 'node:test';
import { createCLICommands, quoteCLIArgument } from '../src/lib/cli-command.ts';

test('POSIX and PowerShell examples quote paths for their own shell', () => {
  const root = "/Users/your name/O'Reilly 東京/Wiki";
  const powershellRoot = "C:\\Users\\your name\\O'Reilly 東京\\Wiki";
  const posix = createCLICommands({ shell: 'posix', root, authOrigin: 'https://auth.dev.test', projectId: 'project-123' });
  const powershell = createCLICommands({ shell: 'powershell', root: powershellRoot, authOrigin: 'https://auth.dev.test', projectId: 'project-123' });

  assert.equal(quoteCLIArgument(root, 'posix'), "'/Users/your name/O'\\''Reilly 東京/Wiki'");
  assert.equal(quoteCLIArgument(powershellRoot, 'powershell'), String.raw`'C:\Users\your name\O''Reilly 東京\Wiki'`);
  assert.match(posix.init, /^\.\/lwc-sync init/);
  assert.match(powershell.init, /^\.\\lwc-sync\.exe init/);
  assert.equal(posix.push, `./lwc-sync push --vault ${quoteCLIArgument(root, 'posix')}`);
  assert.match(powershell.push, /^\.\\lwc-sync\.exe push --vault /);
  assert.equal(posix.pushSync, `${posix.push} --sync`);
});

test('empty roots do not produce copyable init or push commands', () => {
  const commands = createCLICommands({ shell: 'posix', root: '  ', authOrigin: 'https://auth.dev.test', projectId: 'project-123' });
  assert.equal(commands.init, '');
  assert.equal(commands.push, '');
  assert.equal(commands.pushSync, '');
});

test('root paths must be absolute in the selected shell', () => {
  assert.equal(createCLICommands({ shell: 'posix', root: 'relative/wiki', authOrigin: 'https://auth.dev.test', projectId: 'project-123' }).init, '');
  assert.equal(createCLICommands({ shell: 'powershell', root: 'C:\\Users\\you\\Wiki', authOrigin: 'https://auth.dev.test', projectId: 'project-123' }).push.length > 0, true);
  assert.equal(createCLICommands({ shell: 'powershell', root: 'relative\\Wiki', authOrigin: 'https://auth.dev.test', projectId: 'project-123' }).push, '');
});
