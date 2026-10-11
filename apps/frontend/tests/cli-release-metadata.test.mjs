import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import { loadCLIReleaseManifest, validateCLIReleaseManifest } from '../src/lib/cli-release-metadata.ts';

const endpoint = 'https://storage.googleapis.com/llm-wiki-frontend-config-dev/cli/stable/latest.json';
const fixture = JSON.parse(await readFile(new URL('../../../docs/cli-release/latest.example.json', import.meta.url), 'utf8'));

test('shared stable fixture validates with unavailable assets when URLs are absent', () => {
  const manifest = validateCLIReleaseManifest(fixture, endpoint);
  assert.equal(manifest.version, '1.2.3');
  assert.equal(manifest.assets.length, 4);
  assert.ok(manifest.assets.every((asset) => asset.download_url === undefined));
});

test('metadata rejects unsupported, duplicate, and off-origin assets', () => {
  const duplicate = structuredClone(fixture);
  duplicate.assets[1] = { ...duplicate.assets[0] };
  assert.throws(() => validateCLIReleaseManifest(duplicate, endpoint), /Duplicate or unsupported/);

  const offOrigin = structuredClone(fixture);
  offOrigin.assets[0].download_url = `https://downloads.example.test/cli/versions/1.2.3/${offOrigin.assets[0].asset_name}`;
  assert.throws(() => validateCLIReleaseManifest(offOrigin, endpoint), /Untrusted CLI download URL/);

  assert.throws(() => validateCLIReleaseManifest({ ...fixture, channel: 'prerelease' }, endpoint), /Unsupported CLI release metadata/);
});

test('metadata fetch omits credentials and bypasses cached latest data', async () => {
  const original = process.env.NEXT_PUBLIC_CLI_RELEASE_METADATA_URL;
  process.env.NEXT_PUBLIC_CLI_RELEASE_METADATA_URL = endpoint;
  let request;
  try {
    const manifest = await loadCLIReleaseManifest(async (url, options) => {
      request = { url, options };
      return { ok: true, json: async () => fixture };
    });
    assert.equal(manifest?.version, '1.2.3');
    assert.equal(request.url, endpoint);
    assert.equal(request.options.credentials, 'omit');
    assert.equal(request.options.cache, 'no-store');
  } finally {
    if (original === undefined) delete process.env.NEXT_PUBLIC_CLI_RELEASE_METADATA_URL;
    else process.env.NEXT_PUBLIC_CLI_RELEASE_METADATA_URL = original;
  }
});

test('producer-shaped bucket paths load while mismatched paths stay unavailable', async () => {
  const published = structuredClone(fixture);
  published.assets = published.assets.map((asset) => ({
    ...asset,
    download_url: `https://storage.googleapis.com/llm-wiki-frontend-config-dev/cli/versions/${published.version}/${asset.asset_name}`,
  }));
  const original = process.env.NEXT_PUBLIC_CLI_RELEASE_METADATA_URL;
  process.env.NEXT_PUBLIC_CLI_RELEASE_METADATA_URL = endpoint;
  try {
    const valid = await loadCLIReleaseManifest(async () => ({ ok: true, json: async () => published }));
    assert.equal(valid?.assets.length, 4);

    const wrongBucket = structuredClone(published);
    wrongBucket.assets[0].download_url = wrongBucket.assets[0].download_url.replace('llm-wiki-frontend-config-dev', 'another-bucket');
    assert.equal(await loadCLIReleaseManifest(async () => ({ ok: true, json: async () => wrongBucket })), null);

    const wrongPath = structuredClone(published);
    wrongPath.assets[0].download_url = wrongPath.assets[0].download_url.replace('/cli/versions/', '/cli/other/');
    assert.equal(await loadCLIReleaseManifest(async () => ({ ok: true, json: async () => wrongPath })), null);
  } finally {
    if (original === undefined) delete process.env.NEXT_PUBLIC_CLI_RELEASE_METADATA_URL;
    else process.env.NEXT_PUBLIC_CLI_RELEASE_METADATA_URL = original;
  }
});

test('missing endpoint is a safe unavailable state without a network request', async () => {
  const original = process.env.NEXT_PUBLIC_CLI_RELEASE_METADATA_URL;
  delete process.env.NEXT_PUBLIC_CLI_RELEASE_METADATA_URL;
  try {
    assert.equal(await loadCLIReleaseManifest(async () => { throw new Error('must not fetch'); }), null);
  } finally {
    if (original !== undefined) process.env.NEXT_PUBLIC_CLI_RELEASE_METADATA_URL = original;
  }
});
