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

const prelude = `
const import_errors = { NowBuildError: class NowBuildError extends Error {
  constructor(value) { super(value.message); this.code = value.code; }
} };
const process = { env: globalThis.__env };
const resolveProjectCwd = async value => value;
const output_manager_default = {
  error: value => globalThis.__errors.push(value), spinner() {}, stopSpinner() {}, debug() {}, print() {}
};
class ProjectNotFound {}
const isAPIError = () => false;
const isOwnerLookupUnavailableError = () => false;
const code = value => value;
const VERCEL_DIR = '.vercel';
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
const executable = `${prelude}\n${platformEnvSource}\nconst getPlatformEnv2 = getPlatformEnv;\n${linkedProjectSource}\nglobalThis.resolveLinkedProject = getLinkedProject;`;

async function resolve(env, expectedProject = 'prj_Validated', expectedOrg = 'team_Validated',
  cwd = '/offline-fake-root') {
  const context = { __env: { ...env }, __errors: [], __lookups: 0,
    __expectedProject: expectedProject, __expectedOrg: expectedOrg };
  vm.runInNewContext(executable, context, { filename: 'vercel-59.11.7-get-linked-project.js' });
  const result = await context.resolveLinkedProject(
    { cwd, config: { currentTeam: env.VERCEL_TEAM_ID } },
    { scopeIsExplicit: true });
  return { result, errors: context.__errors, lookups: context.__lookups };
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
