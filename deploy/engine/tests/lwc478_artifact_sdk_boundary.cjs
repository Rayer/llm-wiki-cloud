'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const http = require('node:http');
const https = require('node:https');
const path = require('node:path');
const {pathToFileURL} = require('node:url');

const ROOT = path.resolve(__dirname, '../../..');
const SDK_ROOT = path.join(__dirname, 'artifact-sdk', 'node_modules', '@actions', 'artifact');
const SDK_PACKAGE = require(path.join(SDK_ROOT, 'package.json'));
const UPLOAD_SEARCH_SHA256 = '4cbd489c239640073ee1bd17b9f941aabcf34cb7f41d68edd2264ee0438174e9';
if (SDK_PACKAGE.version !== '2.3.2') throw new Error('unexpected retained artifact SDK version');

function requestHost(target, options) {
  if (typeof target === 'string' || target instanceof URL) return new URL(target).hostname;
  if (target && typeof target === 'object') {
    const host = target.hostname || target.host || options?.hostname || options?.host || 'localhost';
    return String(host).replace(/^\[|\]$/g, '').split(':')[0];
  }
  if (typeof target === 'number' && options && typeof options === 'object') {
    return String(options.hostname || options.host || 'localhost').replace(/^\[|\]$/g, '').split(':')[0];
  }
  return 'unknown';
}

function guardHttp(module) {
  const originalRequest = module.request;
  module.request = function (...args) {
    const host = requestHost(args[0], args[1]);
    if (!['127.0.0.1', 'localhost', '::1'].includes(host)) {
      throw new Error(`offline artifact test blocked non-loopback HTTP host: ${host}`);
    }
    return originalRequest.apply(this, args);
  };
  const originalGet = module.get;
  module.get = function (...args) {
    const host = requestHost(args[0], args[1]);
    if (!['127.0.0.1', 'localhost', '::1'].includes(host)) {
      throw new Error(`offline artifact test blocked non-loopback HTTP host: ${host}`);
    }
    return originalGet.apply(this, args);
  };
}

guardHttp(http);
guardHttp(https);
if (typeof globalThis.fetch === 'function') {
  globalThis.fetch = async () => { throw new Error('offline artifact test blocked fetch'); };
}

function listFiles(directory) {
  const files = [];
  for (const entry of fs.readdirSync(directory, {withFileTypes: true})) {
    if (entry.isSymbolicLink()) throw new Error('SDK boundary fixture rejects symlinks');
    const full = path.join(directory, entry.name);
    if (entry.isDirectory()) files.push(...listFiles(full));
    else if (entry.isFile()) files.push(full);
  }
  return files.sort();
}

function writeResult(value) {
  process.stdout.write(`LWC478_RESULT ${JSON.stringify(value)}\n`);
}

async function upload(rootDirectory, archivePath, artifactName) {
  const sourcePath = path.join(ROOT, 'scripts/fixtures/actions-upload-artifact-ea165f8d65b6e75b540449e92b4886f43607fa02/src/shared/search.ts');
  const source = fs.readFileSync(sourcePath);
  if (crypto.createHash('sha256').update(source).digest('hex') !== UPLOAD_SEARCH_SHA256) {
    throw new Error('pinned upload-artifact scalar root fixture digest mismatch');
  }
  const runtimeDirectory = fs.mkdtempSync(path.join(__dirname, 'artifact-sdk', 'runtime-'));
  const searchPath = path.join(runtimeDirectory, 'src', 'shared', 'search.ts');
  fs.mkdirSync(path.dirname(searchPath), {recursive: true});
  fs.writeFileSync(searchPath, source);
  let search;
  try {
    const searchModule = await import(pathToFileURL(searchPath).href);
    search = await searchModule.findFilesToUpload(rootDirectory, false);
  } finally {
    fs.rmSync(runtimeDirectory, {recursive: true, force: true});
  }
  if (path.resolve(search.rootDirectory) !== path.resolve(rootDirectory)) {
    throw new Error(`pinned uploader selected unexpected scalar root: ${search.rootDirectory}`);
  }

  const utility = require(path.join(SDK_ROOT, 'lib/internal/shared/util.js'));
  utility.getBackendIdsFromToken = () => ({
    workflowRunBackendId: 'offline-workflow-run',
    workflowJobRunBackendId: 'offline-workflow-job-run'
  });
  const twirp = require(path.join(SDK_ROOT, 'lib/internal/shared/artifact-twirp-client.js'));
  twirp.internalArtifactTwirpClient = () => ({
    async CreateArtifact(request) {
      if (request.name !== artifactName || request.version !== 4) throw new Error('unexpected create artifact request');
      return {ok: true, signedUploadUrl: 'lwc-offline-transport://artifact-upload'};
    },
    async FinalizeArtifact(request) {
      if (request.name !== artifactName || Number(request.size) < 1) throw new Error('unexpected finalize artifact request');
      return {ok: true, artifactId: '9001'};
    }
  });
  const blobUpload = require(path.join(SDK_ROOT, 'lib/internal/upload/blob-upload.js'));
  blobUpload.uploadZipToBlobStorage = async (signedUrl, zipUploadStream) => {
    if (signedUrl !== 'lwc-offline-transport://artifact-upload') throw new Error('unexpected isolated upload transport');
    const chunks = [];
    for await (const chunk of zipUploadStream) chunks.push(Buffer.from(chunk));
    const archive = Buffer.concat(chunks);
    fs.writeFileSync(archivePath, archive);
    return {
      uploadSize: BigInt(archive.length),
      sha256Hash: crypto.createHash('sha256').update(archive).digest('hex')
    };
  };
  const {DefaultArtifactClient} = require(path.join(SDK_ROOT, 'lib/internal/client.js'));
  const client = new DefaultArtifactClient();
  const response = await client.uploadArtifact(artifactName, search.filesToUpload, search.rootDirectory,
    {retentionDays: 90});
  const archive = fs.readFileSync(archivePath);
  writeResult({
    action: 'upload', sdkVersion: SDK_PACKAGE.version, artifactName, artifactId: response.id,
    rootDirectory: search.rootDirectory, fileCount: search.filesToUpload.length,
    archiveSha256: `sha256:${crypto.createHash('sha256').update(archive).digest('hex')}`,
    archiveSize: archive.length
  });
}

async function download(archivePath, destination, expectedHash, artifactId) {
  const github = require('@actions/github');
  let requestCount = 0;
  let archiveServer;
  github.getOctokit = token => {
    if (token !== 'lwc-offline-token') throw new Error('unexpected fake artifact token');
    return {rest: {actions: {downloadArtifact: async request => {
      if (request.owner !== 'lwc-test' || request.repo !== 'llm-wiki-cloud' ||
          request.artifact_id !== Number(artifactId) || request.archive_format !== 'zip') {
        throw new Error('unexpected offline artifact metadata request');
      }
      requestCount++;
      return {status: 302, headers: {location: `http://127.0.0.1:${archiveServer.address().port}/artifact.zip`}};
    }}}};
  };

  archiveServer = http.createServer((request, response) => {
    if (request.method !== 'GET' || request.url !== '/artifact.zip') {
      response.writeHead(404).end();
      return;
    }
    const stat = fs.statSync(archivePath);
    response.writeHead(200, {'Content-Length': String(stat.size), 'Content-Type': 'application/zip'});
    fs.createReadStream(archivePath).pipe(response);
  });
  await new Promise((resolve, reject) => {
    archiveServer.once('error', reject);
    archiveServer.listen(0, '127.0.0.1', resolve);
  });
  try {
    const {DefaultArtifactClient} = require(path.join(SDK_ROOT, 'lib/internal/client.js'));
    const client = new DefaultArtifactClient();
    const response = await client.downloadArtifact(Number(artifactId), {
      path: destination,
      expectedHash,
      findBy: {token: 'lwc-offline-token', repositoryOwner: 'lwc-test', repositoryName: 'llm-wiki-cloud'}
    });
    writeResult({
      action: 'download', sdkVersion: SDK_PACKAGE.version, artifactId: Number(artifactId),
      destination, expectedHash, digestMismatch: response.digestMismatch, metadataRequestCount: requestCount
    });
  } finally {
    await new Promise(resolve => archiveServer.close(resolve));
  }
}

async function main() {
  const [action, ...args] = process.argv.slice(2);
  if (action === 'upload' && args.length === 3) return upload(args[0], args[1], args[2]);
  if (action === 'download' && args.length === 4) return download(args[0], args[1], args[2], args[3]);
  throw new Error('usage: lwc478_artifact_sdk_boundary.cjs upload <root> <archive> <name> | download <archive> <destination> <sha256> <id>');
}

main().catch(error => {
  console.error(error.stack || String(error));
  process.exitCode = 1;
});
