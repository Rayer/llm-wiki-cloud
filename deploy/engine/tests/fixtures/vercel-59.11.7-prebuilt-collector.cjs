// Offline-only probe of the pinned CLI collector. It stops at hashes-calculated,
// before any Vercel API request or upload can occur.
const Module = require('node:module');
const path = require('node:path');
const { pathToFileURL } = require('node:url');

const [chunk, root] = process.argv.slice(2);
if (!chunk || !root) throw new Error('usage: node probe.cjs PINNED_CHUNK EXTRACTED_ROOT');
const load = Module._load;
Module._load = function (request, parent, isMain) {
  if (request === '@vercel/build-utils') return {};
  return load.call(this, request, parent, isMain);
};

(async () => {
  const bundled = await import(pathToFileURL(chunk));
  const cli = bundled.require_dist();
  const deployment = cli.continueDeployment({
    deploymentId: 'dpl_offline_probe',
    path: root,
    vercelOutputDir: path.join(root, '.vercel', 'output'),
    token: '',
    teamId: '',
    apiUrl: 'http://127.0.0.1:1',
  });
  const { value } = await deployment.next();
  if (value?.type !== 'hashes-calculated') {
    throw new Error(`expected hashes-calculated, received ${value?.type ?? 'no event'}`);
  }
  console.log(`event=${value.type}`);
  console.log(`unique_hashes=${Object.keys(value.payload).length}`);
})().catch((error) => {
  console.log(`collector_error=${error.name}`);
  if (error.code) console.log(`collector_code=${error.code}`);
  if (error.path) console.log(`collector_path=${error.path}`);
  console.log(`collector_message=${String(error.message).split('\n').join('\\n')}`);
  process.exitCode = 1;
});
