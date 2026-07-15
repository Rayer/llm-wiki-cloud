import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

test('LWC-161: developer details load public build metadata independently and remain collapsed', async () => {
  const statusClient = await readFile(
    new URL('../src/components/StatusClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(statusClient, /getBuildInfo/);
  assert.match(statusClient, /type BuildInfo/);
  assert.match(statusClient, /const \[buildInfo, setBuildInfo\] = useState<BuildInfo \| null>\(null\);/);
  assert.match(statusClient, /const \[buildInfoError, setBuildInfoError\] = useState\(''\);/);
  assert.match(statusClient, /const \[showRaw, setShowRaw\] = useState\(false\);/);
  assert.match(statusClient, /getBuildInfo\(\)[\s\S]*?setBuildInfo\(info\)[\s\S]*?\.catch\([\s\S]*?setBuildInfoError/);
  assert.doesNotMatch(statusClient, /getBuildInfo\(\)[\s\S]{0,500}setError\(/);
  assert.match(statusClient, /getBuildInfo\(\)[\s\S]*?if \(!cancelled\) setBuildInfo/);
  assert.match(statusClient, /\{status \? \([\s\S]*?\) : null\}\s*<DeveloperDetails/);
});

test('LWC-161: expanded developer details show only localized allowlisted build fields and diagnostics', async () => {
  const [statusClient, english, traditionalChinese] = await Promise.all([
    readFile(new URL('../src/components/StatusClient.tsx', import.meta.url), 'utf8'),
    readFile(new URL('../src/messages/en.json', import.meta.url), 'utf8').then(JSON.parse),
    readFile(new URL('../src/messages/zh-TW.json', import.meta.url), 'utf8').then(JSON.parse),
  ]);

  const expandedDetails = statusClient.slice(statusClient.indexOf('{showRaw ?'));
  const fields = [
    ['productVersion', 'product_version'],
    ['commit', 'commit'],
    ['branch', 'branch'],
    ['gitTag', 'tag'],
    ['imageTag', 'image_tag'],
    ['cloudRunService', 'service'],
    ['cloudRunRevision', 'revision'],
  ];

  assert.match(statusClient, /JSON\.stringify\(\{ api: status\?\.raw \?\? null, build: buildInfo \}, null, 2\)/);
  assert.match(expandedDetails, /t\('Status\.buildInfoUnavailable'\)/);
  assert.match(expandedDetails, /buildInfo\.tag \|\| '—'/);

  for (const [label, property] of fields) {
    assert.match(expandedDetails, new RegExp(`t\\('Status\\.${label}'\\)`));
    assert.match(expandedDetails, new RegExp(`buildInfo\\.${property}`));
    assert.equal((statusClient.match(new RegExp(`Status\\.${label}`, 'g')) ?? []).length, 1);
    assert.equal(typeof english.Status[label], 'string');
    assert.equal(typeof traditionalChinese.Status[label], 'string');
  }

  assert.equal(english.Status.buildInfoUnavailable, 'Build information is unavailable.');
  assert.equal(traditionalChinese.Status.buildInfoUnavailable, '無法取得建置資訊。');
});
