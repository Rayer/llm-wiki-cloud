export type CLIShell = 'posix' | 'powershell';

export type CLICommandInput = {
  shell: CLIShell;
  root: string;
  authOrigin: string;
  projectId: string;
};

export function quoteCLIArgument(value: string, shell: CLIShell): string {
  if (shell === 'powershell') return `'${value.replaceAll("'", "''")}'`;
  return `'${value.replaceAll("'", "'\\''")}'`;
}

function isAbsoluteRoot(root: string, shell: CLIShell): boolean {
  return shell === 'powershell' ? /^[A-Za-z]:[\\/]|^\\\\/.test(root) : root.startsWith('/');
}

export function createCLICommands(input: CLICommandInput) {
  const executable = input.shell === 'powershell' ? '.\\lwc-sync.exe' : './lwc-sync';
  const quote = (value: string) => quoteCLIArgument(value, input.shell);
  return {
    login: `${executable} auth login --host ${quote(input.authOrigin)} --no-browser`,
    init: isAbsoluteRoot(input.root, input.shell) && input.projectId
      ? `${executable} init --vault ${quote(input.root)} --host ${quote(input.authOrigin)} --project-id ${quote(input.projectId)}`
      : '',
    push: isAbsoluteRoot(input.root, input.shell)
      ? `${executable} push --vault ${quote(input.root)}`
      : '',
    pushSync: isAbsoluteRoot(input.root, input.shell)
      ? `${executable} push --vault ${quote(input.root)} --sync`
      : '',
  };
}
