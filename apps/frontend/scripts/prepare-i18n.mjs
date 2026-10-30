import { execFileSync } from 'node:child_process';
import { existsSync, readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const repositoryRoot = resolve(frontendRoot, '../..');
const preparedRevision = process.env.LWC_I18N_PREPARED_BUNDLE_REVISION;

if (preparedRevision) {
  if (!/^[a-f0-9]{64}$/.test(preparedRevision)) throw new Error('Prepared i18n bundle revision is invalid.');
  const indexPath = resolve(frontendRoot, 'src/generated/i18n/catalog.ts');
  const manifestPath = resolve(frontendRoot, `public/i18n/catalogs/${preparedRevision}.manifest.json`);
  if (!existsSync(indexPath) || !readFileSync(indexPath, 'utf8').includes(`revisions/${preparedRevision}/`)
      || !existsSync(manifestPath)) {
    throw new Error('Prepared i18n catalog does not match the requested build revision.');
  }
} else {
  execFileSync('python3', [
    resolve(repositoryRoot, 'deploy/engine/frontend_i18n.py'),
    '--root', repositoryRoot,
    '--environment', 'local',
    '--output', resolve(frontendRoot, '.build/i18n'),
    '--frontend-root', frontendRoot,
    '--mode', 'offline',
  ], { cwd: repositoryRoot, stdio: 'inherit', env: process.env });
}
