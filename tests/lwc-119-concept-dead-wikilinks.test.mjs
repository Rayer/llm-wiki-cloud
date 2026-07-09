import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

// LWC-119: dead wikilink slug set must be loaded on concept pages too (not only sources)
test('DetailClient fetches concept slugs for source and concept entry types', async () => {
  const detailClient = await readFile(
    new URL('../src/components/DetailClient.tsx', import.meta.url),
    'utf8',
  );

  // Must not gate getConcepts on source-only — concept pages need dead-link detection
  assert.doesNotMatch(
    detailClient,
    /entryType === 'source'\s*\n\s*\? getConcepts/,
  );
  assert.match(
    detailClient,
    /entryType === 'source'\s*\|\|\s*entryType === 'concept'/,
  );
  assert.match(detailClient, /getConcepts\(\)/);
  assert.match(
    detailClient,
    /existingConceptSlugs=\{existingConceptSlugs\}/,
  );
});

test('concept detail page passes entryType concept into DetailClient', async () => {
  const conceptPage = await readFile(
    new URL('../src/app/concepts/[slug]/page.tsx', import.meta.url),
    'utf8',
  );

  assert.match(conceptPage, /entryType="concept"/);
  assert.match(conceptPage, /DetailClient/);
});
