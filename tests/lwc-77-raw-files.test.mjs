import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

test('shell keeps Raw Files off primary navigation while retaining the compatibility route', async () => {
  const shell = await readFile(
    new URL('../src/components/Shell.tsx', import.meta.url),
    'utf8',
  );

  assert.doesNotMatch(shell, /Database/);
  assert.doesNotMatch(shell, /href: '\/raw'/);
  assert.doesNotMatch(shell, /raw: t\('Shell\.raw'\)/);
});

test('raw page renders the RawClient route component', async () => {
  const page = await readFile(
    new URL('../src/app/raw/page.tsx', import.meta.url),
    'utf8',
  );

  assert.match(page, /import \{ RawClient \} from "@\/components\/RawClient";/);
  assert.match(page, /return <RawClient \/>;/);
});

test('raw client renders metadata table with inline preview entrypoints', async () => {
  const client = await readFile(
    new URL('../src/components/RawClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(client, /getRawFiles/);
  assert.match(client, /getRawFilePreview/);
  assert.match(client, /LoadingState/);
  assert.match(client, /ErrorState/);
  assert.match(client, /EmptyState/);
  assert.match(client, /<th[^>]*>\s*Name\s*<\/th>/s);
  assert.match(client, /<th[^>]*>\s*Size\s*<\/th>/s);
  assert.match(client, /<th[^>]*>\s*Updated\s*<\/th>/s);
  assert.match(client, /<th[^>]*>\s*SHA256\s*<\/th>/s);
  assert.match(client, /<th[^>]*>\s*Ingested\s*<\/th>/s);
  assert.match(client, /variant=\{file\.ingested \? 'published' : 'muted'\}/);
  assert.match(client, /openRawPreview/);
  assert.match(client, /<MarkdownView content=\{preview\.content\}/);
  assert.match(client, /<iframe[\s\S]*?srcDoc=\{sanitizeRawHtml\(preview\.content\)\}/);
  assert.match(client, /function sanitizeRawHtml\(html: string\)/);
  assert.ok(
    client.includes("return html.replace(/<script\\b[\\s\\S]*?<\\/script>/gi, '');"),
    'sanitizeRawHtml should strip script tags',
  );
  assert.match(client, /downloadRawFile/);
  assert.match(client, /rawPreviewKind\(file\.name\)/);
});

test('raw client reloads files when the current project changes', async () => {
  const client = await readFile(
    new URL('../src/components/RawClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(client, /import \{ useWorkspace \} from '\.\/WorkspaceProvider';/);
  assert.match(client, /const \{\s*currentProject\s*\} = useWorkspace\(\);/s);
  assert.match(
    client,
    /useEffect\(\(\) => \{[\s\S]*?getRawFiles\(\)[\s\S]*?\}, \[currentProject\]\);/,
  );
});
