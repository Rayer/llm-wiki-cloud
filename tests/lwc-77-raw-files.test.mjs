import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

test('shell places Raw navigation between Concepts and Status', async () => {
  const shell = await readFile(
    new URL('../src/components/Shell.tsx', import.meta.url),
    'utf8',
  );

  assert.match(shell, /import \{[^}]*Database[^}]*\} from 'lucide-react';/s);
  assert.match(shell, /\{ href: '\/raw', label: t\('Shell\.raw'\), icon: Database \}/);

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

test('raw client renders metadata table without download or preview actions', async () => {
  const client = await readFile(
    new URL('../src/components/RawClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(client, /getRawFiles/);
  assert.match(client, /LoadingState/);
  assert.match(client, /ErrorState/);
  assert.match(client, /EmptyState/);
  assert.match(client, /<th[^>]*>\s*Name\s*<\/th>/s);
  assert.match(client, /<th[^>]*>\s*Size\s*<\/th>/s);
  assert.match(client, /<th[^>]*>\s*Updated\s*<\/th>/s);
  assert.match(client, /<th[^>]*>\s*SHA256\s*<\/th>/s);
  assert.match(client, /<th[^>]*>\s*Ingested\s*<\/th>/s);
  assert.match(client, /variant=\{file\.ingested \? 'published' : 'muted'\}/);
  assert.doesNotMatch(client, /download/i);
  assert.doesNotMatch(client, /preview/i);
});
