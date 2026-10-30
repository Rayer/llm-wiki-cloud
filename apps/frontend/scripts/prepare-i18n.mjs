import { execFileSync } from 'node:child_process';
import { existsSync, readFileSync } from 'node:fs';
import { delimiter, dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const repositoryRoot = resolve(frontendRoot, '../..');
const preparedRevision = process.env.LWC_I18N_PREPARED_BUNDLE_REVISION;

function prepareBuildTools(env) {
  const goMod = readFileSync(resolve(repositoryRoot, 'apps/bff/go.mod'), 'utf8');
  const requiredGo = goMod.match(/^go\s+(\d+\.\d+)/m)?.[1];
  if (!requiredGo) throw new Error('Go version requirement is unavailable.');

  let installedGo = '';
  try {
    installedGo = execFileSync('go', ['version'], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] });
  } catch {}
  if (installedGo.match(/\bgo(\d+\.\d+)\.\d+\b/)?.[1] !== requiredGo) {
    const goRoot = resolve(repositoryRoot, '.build/tools/go');
    execFileSync('bash', [resolve(repositoryRoot, 'scripts/ensure-go.sh'), goRoot], { stdio: 'inherit' });
    env.PATH = [resolve(goRoot, 'bin'), env.PATH].filter(Boolean).join(delimiter);
  }

  if (!env.PKL_BIN) {
    let installedPkl = '';
    try {
      installedPkl = execFileSync('pkl', ['--version'], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] });
    } catch {}
    if (/^Pkl 0\.32\.1(?:\s|$)/m.test(installedPkl)) {
      env.PKL_BIN = 'pkl';
    } else {
      const pkl = resolve(repositoryRoot, '.build/tools/pkl');
      execFileSync('bash', [resolve(repositoryRoot, 'scripts/ensure-pkl.sh'), pkl], { stdio: 'inherit' });
      env.PKL_BIN = pkl;
    }
  }
  env.PKL_CACHE_DIR ||= resolve(repositoryRoot, '.build/cache/pkl-packages');
  env.GOTOOLCHAIN ||= 'local';
}

if (preparedRevision) {
  if (!/^[a-f0-9]{64}$/.test(preparedRevision)) throw new Error('Prepared i18n bundle revision is invalid.');
  const indexPath = resolve(frontendRoot, 'src/generated/i18n/catalog.ts');
  const manifestPath = resolve(frontendRoot, `public/i18n/catalogs/${preparedRevision}.manifest.json`);
  if (!existsSync(indexPath) || !readFileSync(indexPath, 'utf8').includes(`revisions/${preparedRevision}/`)
      || !existsSync(manifestPath)) {
    throw new Error('Prepared i18n catalog does not match the requested build revision.');
  }
} else {
  const buildEnv = { ...process.env };
  for (const name of Object.keys(buildEnv)) {
    if (/(?:api[_-]?key|token|secret|password|credential)/i.test(name)) delete buildEnv[name];
  }
  for (const name of ['VERCEL_PROJECT_ID', 'VERCEL_ORG_ID', 'VERCEL_TEAM_ID', 'NOW_ORG_ID', 'NOW_PROJECT_ID']) {
    delete buildEnv[name];
  }
  buildEnv.GOOGLE_APPLICATION_CREDENTIALS = '/dev/null';
  prepareBuildTools(buildEnv);
  execFileSync('python3', [
    resolve(repositoryRoot, 'deploy/engine/frontend_i18n.py'),
    '--root', repositoryRoot,
    '--environment', 'local',
    '--output', resolve(frontendRoot, '.build/i18n'),
    '--frontend-root', frontendRoot,
    '--mode', 'offline',
  ], { cwd: repositoryRoot, stdio: 'inherit', env: buildEnv });
}
