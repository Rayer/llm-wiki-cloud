import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import { basename, dirname, join, relative } from 'node:path';
import { pathToFileURL } from 'node:url';

const scratch = '/Users/rayer/.hermes/profiles/lwc-tpm/cache/scratch/lwc358-prerender-probe-20261003-02';
const root = process.argv[2];
assert.ok(root, 'fixture root required');
const require = createRequire(import.meta.url);
const expected = {
  schema_version: 1,
  api_url: process.env.NEXT_PUBLIC_API_URL,
  auth_url: process.env.NEXT_PUBLIC_AUTH_URL,
};
assert.ok(expected.api_url && expected.auth_url, 'allowlisted test target env required');

const pkgPath = join(root, 'package.json');
const pkg = JSON.parse(readFileSync(pkgPath, 'utf8'));
pkg.dependencies = { ...pkg.dependencies, next: '16.2.7' };
writeFileSync(pkgPath, `${JSON.stringify(pkg)}\n`);
const bodyPath = join(root, '.next/server/app/build-config.json.body');
const preAdapterBodyBytes = readFileSync(bodyPath);
const initialBody = JSON.parse(preAdapterBodyBytes.toString('utf8'));
assert.deepEqual(initialBody, expected, 'real Next body must match the isolated build target');

const adapter = require(join(scratch, 'node_modules/@vercel/next'));
assert.equal(adapter.version, 2, 'expected pinned @vercel/next build API');
const buildResult = await adapter.build({
  workPath: root,
  repoRootPath: root,
  entrypoint: 'package.json',
  files: {},
  config: { framework: 'nextjs', installCommand: '' },
  meta: {},
});
const bodyBytes = readFileSync(bodyPath);
assert.deepEqual(JSON.parse(bodyBytes.toString('utf8')), expected, 'adapter build must produce the target body');
const routeKey = 'build-config.json';
const prerender = buildResult.output?.[routeKey];
assert.ok(prerender, `adapter output must include ${routeKey}`);
assert.equal(prerender.constructor?.name, 'Prerender', 'adapter must emit its actual Prerender type');
assert.equal(prerender.fallback?.type, 'FileFsRef', 'adapter fallback must be a filesystem reference');
assert.equal(basename(prerender.fallback.fsPath), 'build-config.json.body');
assert.equal(relative(root, prerender.fallback.fsPath), '.next/server/app/build-config.json.body');

const outputDir = join(root, '.vercel/output');
assert.equal(existsSync(outputDir), false, 'writer target must be new');
const cli = await import(pathToFileURL(join(scratch, 'node_modules/vercel/dist/chunks/chunk-QHS645AZ.js')));
await cli.writeBuildResult({
  repoRootPath: root,
  outputDir,
  buildResult,
  build: { use: '@vercel/next' },
  builder: { version: 2 },
  builderPkg: { name: '@vercel/next' },
  vercelConfig: {},
  standalone: false,
  workPath: root,
});

const functionDir = join(outputDir, 'functions/build-config.json.func');
const fallbackPath = join(outputDir, 'functions/build-config.json.prerender-fallback.body');
const descriptorPath = join(outputDir, 'functions/build-config.json.prerender-config.json');
const staticPath = join(outputDir, 'static/build-config.json');
assert.ok(existsSync(functionDir), 'official writer must write the route function directory');
assert.ok(existsSync(fallbackPath), 'official writer must write the descriptor-bound fallback');
assert.ok(existsSync(descriptorPath), 'official writer must write the prerender descriptor');
assert.equal(existsSync(staticPath), false, 'Prerender must not be mistaken for a static File');
const descriptor = JSON.parse(readFileSync(descriptorPath, 'utf8'));
const fallbackRef = descriptor.fallback;
assert.equal(typeof fallbackRef, 'object');
assert.equal(basename(fallbackRef.fsPath), basename(fallbackPath), 'descriptor must bind the route fallback basename');
const fallbackBytes = readFileSync(fallbackPath);
assert.deepEqual(JSON.parse(fallbackBytes.toString('utf8')), expected, 'written fallback must retain exact target JSON');
assert.deepEqual(fallbackBytes, bodyBytes, 'writer must preserve the actual Next body bytes');
const functionConfigPath = join(functionDir, '.vc-config.json');
const functionConfig = existsSync(functionConfigPath) ? JSON.parse(readFileSync(functionConfigPath, 'utf8')) : null;

console.log(JSON.stringify({
  next_body: { exists: true, bytes: bodyBytes.length, generated_by_adapter_build: true, exact_target_match: true },
  adapter: {
    package: '@vercel/next@11.0.2',
    output_key: routeKey,
    output_type: prerender.constructor.name,
    fallback_type: prerender.fallback.type,
    fallback_source: relative(root, prerender.fallback.fsPath),
  },
  cli_writer: {
    package: 'vercel@59.11.7',
    function_path: relative(outputDir, functionDir),
    function_dir_exists: true,
    function_config_path: functionConfig ? relative(outputDir, functionConfigPath) : null,
    function_config_keys: functionConfig ? Object.keys(functionConfig).sort() : [],
    function_handler: functionConfig?.handler ?? null,
    function_runtime: functionConfig?.runtime ?? null,
    descriptor_path: relative(outputDir, descriptorPath),
    descriptor_keys: Object.keys(descriptor).sort(),
    descriptor_fallback_keys: Object.keys(fallbackRef).sort(),
    descriptor_fallback_basename: basename(fallbackRef.fsPath),
    fallback_path: relative(outputDir, fallbackPath),
    fallback_byte_identical_to_next_body: true,
    fallback_exact_target_match: true,
    static_path_exists: false,
  },
}, null, 2));
