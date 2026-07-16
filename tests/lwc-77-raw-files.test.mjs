import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

test('shell places Raw navigation between Concepts and Status', async () => {
  const shell = await readFile(
    new URL('../src/components/Shell.tsx', import.meta.url),
    'utf8',
  );

  assert.match(shell, /import \{[^}]*Database[^}]*\} from 'lucide-react';/s);
  assert.match(
    shell,
    /\{ href: '\/raw', label: t\('Shell\.raw'\), icon: Database(?:, countKey: 'raw')? \}/,
  );

  const conceptsIndex = shell.indexOf("href: '/concepts'");
  const rawIndex = shell.indexOf("href: '/raw'");
  const statusIndex = shell.indexOf("href: '/status'");

  assert.ok(conceptsIndex >= 0, 'concepts nav item missing');
  assert.ok(rawIndex > conceptsIndex, 'raw nav item should appear after concepts');
  assert.ok(statusIndex > rawIndex, 'status nav item should appear after raw');
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
