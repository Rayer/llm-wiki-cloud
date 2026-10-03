'use strict';

const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const fixtures = path.join(__dirname, 'fixtures');
function source(name, expected) {
  const text = fs.readFileSync(path.join(fixtures, name), 'utf8');
  assert.ok(text.endsWith('\n'));
  const exact = text.slice(0, -1);
  assert.equal(crypto.createHash('sha256').update(exact).digest('hex'), expected);
  return exact;
}

const platformEnvSource = source(
  'build-utils-14.9.1-get-platform-env.js',
  'ef25d645045be0d0f21a9f362c4450c6e732e4b56aacb021f49046747c8ee0e5');
const linkedProjectSource = source(
  'vercel-59.11.7-get-linked-project.js',
  '2e749ec1e3821d3f3ab10d6a1579b8eda9d8676350925981cd58e1cfe269a364');
const pullPathSource = source(
  'vercel-59.11.7-env-pull-path.js',
  'a76939e5e3a9e549c7addaeae102c88c048bb7c475ba1af4bee3ac44b2234f7e');
const buildRootSource = source(
  'vercel-59.11.7-build-root.js',
  '0717a88d30e4a55ac51f0bfa4d12df87d9ded9c7416d9b8ec9b57ee2aee3ddb7');
const buildCallerSource = source(
  'vercel-59.11.7-build-caller.js',
  'b0aa8d6efad9e8280e385d42ac8a63bdeeef25dbc3f574c4c38442c1c1c1738a');

const prelude = `
const import_errors = { NowBuildError: class NowBuildError extends Error {
  constructor(value) { super(value.message); this.code = value.code; }
} };
const process = { env: globalThis.__env };
const resolveProjectCwd = async value => value;
const output_manager_default = {
  error: value => globalThis.__errors.push(value), spinner() {}, stopSpinner() {}, debug() {}, print() {}, log() {}
};
class ProjectNotFound {}
const isAPIError = () => false;
const isOwnerLookupUnavailableError = () => false;
const code = value => value;
const VERCEL_DIR = '.vercel';
const VERCEL_DIR_PROJECT = 'project.json';
const join = path.join;
const import_fs_extra = { outputJSON: async (file, value, options) => {
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.writeFileSync(file, JSON.stringify(value, null, options.spaces));
} };
const pullAllEnvFiles = async (_environment, _client, _link, _flags, directory) => {
  globalThis.__pullEnvDirectory = directory;
  return 0;
};
const stamp_default = () => () => '';
const prependEmoji = value => value;
const emoji = () => '';
const import_chalk = { default: { bold: value => value, gray: value => value } };
const humanizePath = value => value;
const detectExplicitScope = () => true;
const getProjectLink = async () => { throw new Error('unexpected local-link lookup'); };
const getOrgById = async (client, id) => { globalThis.__lookups++; return { id, slug: 'test-team' }; };
const getProjectByNameOrId = async (client, id, orgId) => {
  globalThis.__lookups++;
  if (id !== globalThis.__expectedProject || orgId !== globalThis.__expectedOrg) {
    throw new Error('unexpected project identity');
  }
  return { id, accountId: orgId, name: 'test-project', rootDirectory: 'apps/frontend' };
};
`;
const executable = `${prelude}\n${platformEnvSource}\nconst getPlatformEnv2 = getPlatformEnv;\n${linkedProjectSource}
const originalGetLinkedProject = getLinkedProject;
getLinkedProject = async (...args) => {
  const result = await originalGetLinkedProject(...args);
  globalThis.__lastLinkedProject = result;
  return result;
};
${pullPathSource}
globalThis.resolveLinkedProject = getLinkedProject;
globalThis.runPinnedPull = async cwd => {
  const client = { cwd, config: { currentTeam: globalThis.__env.VERCEL_TEAM_ID }, nonInteractive: true };
  const exitCode = await pullCommandLogic(client, cwd, true, 'preview', {}, undefined);
  return { exitCode, currentTeam: client.config.currentTeam };
};`;

async function resolve(env, expectedProject = 'prj_Validated', expectedOrg = 'team_Validated',
  cwd = '/offline-fake-root') {
  const context = { __env: { ...env }, __errors: [], __lookups: 0,
    __expectedProject: expectedProject, __expectedOrg: expectedOrg, fs, path };
  vm.runInNewContext(executable, context, { filename: 'vercel-59.11.7-get-linked-project.js' });
  const result = await context.resolveLinkedProject(
    { cwd, config: { currentTeam: env.VERCEL_TEAM_ID } },
    { scopeIsExplicit: true });
  return { result, errors: context.__errors, lookups: context.__lookups };
}

async function simulatePull(env, cwd, expectedProject, expectedOrg) {
  const context = { __env: { ...env }, __errors: [], __lookups: 0,
    __expectedProject: expectedProject, __expectedOrg: expectedOrg, fs, path };
  vm.runInNewContext(executable, context, { filename: 'vercel-59.11.7-env-pull-path.js' });
  const pull = await context.runPinnedPull(cwd);
  const linked = context.__lastLinkedProject;
  const rootLink = path.join(cwd, '.vercel/project.json');
  const nestedLink = path.join(cwd, 'apps/frontend/.vercel/project.json');
  assert.equal(pull.exitCode, 0);
  assert.equal(linked.status, 'linked');
  assert.equal(linked.project.id, expectedProject);
  assert.equal(linked.project.rootDirectory, 'apps/frontend');
  assert.equal(linked.repoRoot, undefined);
  assert.equal(linked.projectRootDirectory, undefined);
  assert.equal(context.__pullEnvDirectory, cwd);
  assert.equal(fs.existsSync(rootLink), true);
  assert.equal(fs.existsSync(nestedLink), false);
  const settings = JSON.parse(fs.readFileSync(rootLink, 'utf8'));
  assert.equal(settings.projectId, expectedProject);
  assert.equal(settings.orgId, expectedOrg);
  assert.equal(settings.settings.rootDirectory, 'apps/frontend');
  return { linkPath: '.vercel/project.json', pullDirectory: '.', settings };
}

function pinnedResolveBuildRoot(cwd, repositoryRoot, rootDirectorySetting, allowedRoot = repositoryRoot) {
  const withinRepository = value => {
    const relative = path.relative(allowedRoot, path.resolve(value));
    return relative === '' || (relative !== '..' && !relative.startsWith('..'+path.sep) && !path.isAbsolute(relative));
  };
  const context = {
    existsSync: value => withinRepository(value) && fs.existsSync(value),
    readFileSync2: (value, ...args) => {
      assert.ok(withinRepository(value), 'source resolver may inspect only its isolated repository fixture');
      return fs.readFileSync(value, ...args);
    },
    join2: path.join, parse: path.parse, dirname: path.dirname, relative: path.relative,
    import_minimatch: { default: (value, pattern) => value === pattern },
    // The exercised fixtures use literal npm workspace entries. pnpm parsing and glob semantics are outside this seam.
    js_yaml_default: { load: () => { throw new Error('pnpm workspace stub not exercised'); } },
  };
  vm.runInNewContext(buildRootSource + '\nglobalThis._resolvePerDirectoryLinkRoot = resolvePerDirectoryLinkRoot;',
    context, { filename: 'vercel-59.11.7-build-root.js' });
  const root = context._resolvePerDirectoryLinkRoot(cwd, rootDirectorySetting);
  return { root, output: path.join(root.repoRoot, root.resolvedRootDirectory, '.vercel/output') };
}

function pinnedBuildCaller(cwd, projectLinkPath, allowedRoot) {
  const localProject = JSON.parse(fs.readFileSync(projectLinkPath, 'utf8'));
  const link = { repoRoot: localProject.repoRoot,
    projectRootDirectory: localProject.projectRootDirectory };
  const project = { settings: localProject.settings };
  const context = {
    existsSync: value => {
      const relative = path.relative(allowedRoot, path.resolve(value));
      return (relative === '' || (relative !== '..' && !relative.startsWith('..' + path.sep) &&
        !path.isAbsolute(relative))) && fs.existsSync(value);
    },
    readFileSync2: (value, ...args) => {
      assert.ok(path.relative(allowedRoot, path.resolve(value)).split(path.sep)[0] !== '..',
        'pinned resolver reads only within the offline fixture');
      return fs.readFileSync(value, ...args);
    },
    join2: path.join, join3: path.join, parse: path.parse, dirname: path.dirname,
    relative: path.relative, path,
    import_minimatch: { default: (value, pattern) => value === pattern },
    js_yaml_default: { load: () => { throw new Error('pnpm workspace stub not exercised'); } },
  };
  vm.runInNewContext(buildRootSource + '\n' + buildCallerSource + `
globalThis.__resolve = resolvePerDirectoryLinkRoot;
globalThis.__caller = pinnedBuildCallerContext;
globalThis.__doBuildWorkPath = pinnedDoBuildWorkPath;
globalThis.__defaultOutputDir = pinnedDefaultOutputDir;`,
  context, { filename: 'vercel-59.11.7-build-root-and-caller.js' });
  const state = context.__caller(cwd, link, project, context.__resolve);
  return {
    cwd: state.cwd,
    projectRootDirectory: state.projectRootDirectory,
    settingsRoot: state.project.settings.rootDirectory,
    workPath: context.__doBuildWorkPath(state.cwd, state.project, path.join),
    outputDir: context.__defaultOutputDir(state.cwd, state.projectRootDirectory, path.join),
  };
}

function pinnedBuildOutput(cwd, repositoryRoot) {
  const projectSettings = JSON.parse(fs.readFileSync(path.join(cwd, '.vercel/project.json'), 'utf8'));
  const resolved = pinnedResolveBuildRoot(cwd, repositoryRoot, projectSettings.settings.rootDirectory);
  return {
    root: resolved.root,
    output: resolved.output,
    settings: projectSettings.settings,
  };
}

async function main() {
  if (process.argv[2] === '--assert-linked-env') {
    const input = JSON.parse(fs.readFileSync(0, 'utf8'));
    assert.equal(input.env.VERCEL_PROJECT_ID, input.expected.project);
    assert.equal(input.env.VERCEL_ORG_ID, input.expected.team);
    assert.equal(input.env.VERCEL_TEAM_ID, input.expected.team);
    assert.equal(input.env.NOW_ORG_ID, undefined);
    assert.equal(input.env.NOW_PROJECT_ID, undefined);
    const linked = await resolve(input.env, input.expected.project, input.expected.team);
    assert.equal(linked.result.status, 'linked');
    assert.equal(linked.result.project.id, input.expected.project);
    assert.equal(linked.result.org.id, input.expected.team);
    assert.equal(linked.lookups, 2);
    console.log('Provider.prepare child identity follows pinned Vercel linked branch: PASS');
    return;
  }

  if (process.argv[2] === '--simulate-env-pull') {
    const input = JSON.parse(fs.readFileSync(0, 'utf8'));
    const result = await simulatePull(input.env, input.cwd,
      input.expected.project, input.expected.team);
    console.log(JSON.stringify({ linkPath: result.linkPath, pullDirectory: result.pullDirectory }));
    return;
  }

  if (process.argv[2] === '--simulate-project-cwd-pull') {
    const input = JSON.parse(fs.readFileSync(0, 'utf8'));
    const actualCheckout = path.resolve(__dirname, '../../..');
    assert.equal(fs.existsSync(path.join(actualCheckout, 'package.json')), false,
      'checked-in repository root must remain without a workspace package');
    assert.equal(fs.existsSync(path.join(actualCheckout, 'pnpm-workspace.yaml')), false,
      'checked-in repository root must remain without a pnpm workspace');
    const pull = await simulatePull(input.env, input.cwd, input.expected.project, input.expected.team);
    const build = pinnedBuildOutput(input.cwd, input.repositoryRoot);
    const repoCwdBuild = pinnedResolveBuildRoot(input.repositoryRoot, input.repositoryRoot, 'apps/frontend');
    const outputRelative = path.relative(input.cwd, build.output).split(path.sep).join('/');
    const repoCwdOutputRelative = path.relative(input.repositoryRoot, repoCwdBuild.output).split(path.sep).join('/');
    assert.equal(outputRelative, '.vercel/output');
    assert.equal(repoCwdOutputRelative, '.vercel/output');
    assert.notEqual(path.resolve(repoCwdBuild.output), path.resolve(build.output));
    assert.equal(pull.linkPath, '.vercel/project.json');
    assert.equal(build.settings.rootDirectory, 'apps/frontend');
    const expectedWorkspaceRoot = input.workspaceCase === 'claiming-ancestor' ? '' : 'apps/frontend';
    assert.equal(path.relative(input.repositoryRoot, build.root.repoRoot).split(path.sep).join('/'),
      expectedWorkspaceRoot);
    assert.equal(build.root.resolvedRootDirectory, expectedWorkspaceRoot ? '' : 'apps/frontend');
    assert.equal(path.relative(input.repositoryRoot, build.output).split(path.sep).join('/'),
      'apps/frontend/.vercel/output');
    assert.equal(fs.existsSync(path.join(input.repositoryRoot, '.vercel/project.json')), false,
      'pull must write the selected project link in the configured project directory');
    console.log(JSON.stringify({ linkPath: pull.linkPath, pullDirectory: pull.pullDirectory,
      outputPath: outputRelative, workspaceRoot: expectedWorkspaceRoot || '.',
      resolvedRootDirectory: build.root.resolvedRootDirectory,
      repoCwdOutputPath: repoCwdOutputRelative }));
    return;
  }

  if (process.argv[2] === '--simulate-project-cwd-build') {
    const input = JSON.parse(fs.readFileSync(0, 'utf8'));
    const projectLinkPath = path.join(input.cwd, '.vercel/project.json');
    const projectLink = JSON.parse(fs.readFileSync(projectLinkPath, 'utf8'));
    assert.equal(Object.prototype.hasOwnProperty.call(projectLink.settings, 'rootDirectory'), false,
      'Provider.prepare must remove only the pulled local rootDirectory for build');
    const caller = pinnedBuildCaller(input.cwd, projectLinkPath, input.repositoryRoot);
    const build = pinnedBuildOutput(input.cwd, input.repositoryRoot);
    const workPath = path.resolve(caller.workPath);
    const expectedProjectRoot = path.resolve(input.projectRoot);
    assert.equal(workPath, expectedProjectRoot,
      'pinned doBuild workPath must resolve to the validated Frontend project root');
    assert.equal(fs.existsSync(path.join(workPath, 'package.json')), true,
      'pinned doBuild workPath must contain the actual Frontend package');
    assert.equal(path.resolve(caller.outputDir),
      path.resolve(input.repositoryRoot, input.configuredRoot, '.vercel/output'),
      'pinned default output must remain under the configured Frontend root');
    assert.equal(path.resolve(caller.outputDir), path.resolve(build.output));
    const outputRelative = path.relative(input.cwd, build.output).split(path.sep).join('/');
    assert.equal(outputRelative, '.vercel/output');
    assert.equal(path.relative(input.repositoryRoot, build.output).split(path.sep).join('/'),
      path.posix.join(input.configuredRoot, '.vercel/output'));
    if (input.outputCase !== 'none') {
      const staticDirectory = path.join(build.output, 'static');
      fs.mkdirSync(staticDirectory, { recursive: true });
      if (input.outputCase !== 'missing') {
        fs.writeFileSync(path.join(staticDirectory, 'build-config.json'), JSON.stringify(input.config));
      }
    }
    console.log(JSON.stringify({ workPathContainsPackage: true, duplicateRoot: false,
      outputPath: outputRelative,
      resolvedRootDirectory: build.root.resolvedRootDirectory }));
    return;
  }

  if (process.argv[2] === '--classify-runtime-env') {
    const input = JSON.parse(fs.readFileSync(0, 'utf8'));
    const localProject = JSON.parse(fs.readFileSync(path.join(input.cwd, '.vercel/project.json'), 'utf8'));
    assert.equal(localProject.projectId, input.expected.project);
    assert.equal(localProject.orgId, input.expected.team);
    const resolved = await resolve(input.env, input.expected.project, input.expected.team, input.cwd);
    if (resolved.result.status === 'error' && resolved.result.exitCode === 1 && resolved.lookups === 0) {
      console.log('pair-incomplete');
      return;
    }
    if (resolved.result.status === 'linked' && resolved.lookups === 2 &&
        resolved.result.project.id === input.expected.project && resolved.result.org.id === input.expected.team) {
      console.log('linked');
      return;
    }
    throw new Error('unexpected pinned resolver result');
  }

  const incomplete = await resolve({
    VERCEL_PROJECT_ID: 'prj_Validated', VERCEL_TEAM_ID: 'team_Validated'
  });
  assert.equal(incomplete.result.status, 'error');
  assert.equal(incomplete.result.exitCode, 1);
  assert.match(incomplete.errors.join(' '), /VERCEL_ORG_ID/);
  assert.equal(incomplete.lookups, 0);
  console.log('PROJECT present + ORG absent + TEAM and --scope context is rejected before lookup: PASS');

  const mapped = await resolve({
    VERCEL_PROJECT_ID: 'prj_Validated', VERCEL_ORG_ID: 'team_Validated',
    VERCEL_TEAM_ID: 'team_Validated'
  });
  assert.equal(mapped.result.status, 'linked');
  assert.equal(mapped.result.project.id, 'prj_Validated');
  assert.equal(mapped.result.org.id, 'team_Validated');
  assert.equal(mapped.lookups, 2);
  console.log('Validated TEAM mapped to ORG + selected PROJECT uses the same linked branch: PASS');

  for (const env of [
    { VERCEL_PROJECT_ID: 'prj_Validated', NOW_PROJECT_ID: 'prj_Other',
      VERCEL_ORG_ID: 'team_Validated', VERCEL_TEAM_ID: 'team_Validated' },
    { VERCEL_PROJECT_ID: 'prj_Validated', VERCEL_ORG_ID: 'team_Validated',
      NOW_ORG_ID: 'team_Other', VERCEL_TEAM_ID: 'team_Validated' }
  ]) {
    await assert.rejects(resolve(env), error => error.code === 'CONFLICTING_ENV_VAR_NAMES');
  }
  console.log('Pinned build-utils rejects conflicting inherited NOW aliases: PASS');
}

main().catch(error => { console.error(error); process.exitCode = 1; });
