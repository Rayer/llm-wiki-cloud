// Bounded, network-free source fixture for the pinned collector behavior.
// The map expansion follows chunk-G3PXSXIB.js buildFileTree2 (59.11.7,
// filePathMap refs) and the hash loop follows its hashes() implementation.
// The live pinned bundle is additionally exercised when LWC_TEST_VERCEL_CHUNK is set.
const crypto = require('node:crypto');
const fs = require('node:fs');
const fsp = fs.promises;
const path = require('node:path');

const MAX_BUFFER_FILE_SIZE = 2 ** 31 - 1;
function hash(buf) {
  return crypto.createHash('sha1').update(Uint8Array.from(buf)).digest('hex');
}
async function hashFile(file) {
  const digest = crypto.createHash('sha1');
  for await (const chunk of fs.createReadStream(file)) digest.update(Uint8Array.from(chunk));
  return digest.digest('hex');
}
async function hashes(files) {
  const map = new Map();
  await Promise.all(files.map(async (name) => {
    const stat = await fsp.lstat(name);
    let data;
    let size;
    let h;
    if (!stat.isDirectory()) {
      if (stat.isSymbolicLink()) {
        data = Buffer.from(await fsp.readlink(name), 'utf8');
        size = data.length;
        h = hash(data);
      } else if (stat.size > MAX_BUFFER_FILE_SIZE) {
        size = stat.size;
        h = await hashFile(name);
      } else {
        data = await fsp.readFile(name);
        size = data.length;
        h = hash(data);
      }
    }
    const entry = map.get(h);
    if (entry) {
      const names = new Set(entry.names);
      names.add(name);
      entry.names = [...names];
    } else {
      map.set(h, { names: [name], data, mode: stat.mode, size });
    }
  }));
  return map;
}

async function filesUnder(dir) {
  const files = [];
  for (const entry of await fsp.readdir(dir, { withFileTypes: true })) {
    const name = path.join(dir, entry.name);
    if (entry.isDirectory()) files.push(...await filesUnder(name));
    else files.push(name);
  }
  return files;
}

async function main(root) {
  const output = path.join(root, '.vercel', 'output');
  const fileList = await filesUnder(output);
  const refs = new Set();
  for (const file of fileList.filter((name) => path.basename(name) === '.vc-config.json')) {
    const config = JSON.parse(await fsp.readFile(file, 'utf8'));
    if (!config.filePathMap) continue;
    for (const value of Object.values(config.filePathMap)) {
      const absPath = path.join(root, value);
      const rel = path.relative(root, absPath);
      if (rel.startsWith('..') || path.isAbsolute(rel)) continue;
      refs.add(absPath);
    }
  }
  const allFiles = [...new Set([...fileList, ...refs])];
  const fileHashes = await hashes(allFiles);
  console.log('event=hashes-calculated');
  console.log(`map_refs=${refs.size}`);
  console.log(`hashed_paths=${allFiles.length}`);
  console.log(`unique_hashes=${fileHashes.size}`);
}

const root = process.argv[2];
if (!root) throw new Error('usage: node probe.cjs EXTRACTED_ROOT');
main(root).catch((error) => {
  console.error(`${error.code || error.name}: ${error.message}`);
  process.exitCode = 1;
});
