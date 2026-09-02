import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

test('entry cards expose type borders and staggered fade-in animation', async () => {
  const entryCard = await readFile(
    new URL('../src/components/EntryCard.tsx', import.meta.url),
    'utf8',
  );
  const listClient = await readFile(
    new URL('../src/components/ListClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(entryCard, /index = 0/);
  assert.match(entryCard, /animationDelay: `\$\{index \* 50\}ms`/);
  assert.match(entryCard, /animate-fade-in/);
  assert.match(entryCard, /border-l-\[3px\]/);
  assert.match(entryCard, /entryType === 'source'[\s\S]*border-l-blue-400/);
  assert.match(entryCard, /entryType === 'concept'[\s\S]*border-l-emerald-400/);
  assert.match(listClient, /filtered\.map\(\(entry, index\) =>/);
  assert.match(listClient, /index=\{index\}/);
});

test('home first-run and result cards use staggered entrance polish', async () => {
  const homeClient = await readFile(
    new URL('../src/components/HomeClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(homeClient, /latestConcepts\.map\(\(concept, index\) =>/);
  assert.match(homeClient, /results\.map\(\(result, index\) =>/);
  assert.match(homeClient, /animationDelay: `\$\{index \* 50\}ms`/);
  assert.match(homeClient, /border-l-\[3px\]/);
  assert.match(homeClient, /type === 'source'[\s\S]*border-l-blue-400/);
  assert.match(homeClient, /type === 'concept'[\s\S]*border-l-emerald-400/);
});

test('loading state renders pulse skeleton cards without spinner UI', async () => {
  const states = await readFile(
    new URL('../src/components/States.tsx', import.meta.url),
    'utf8',
  );
  const skeleton = await readFile(
    new URL('../src/components/ui/Skeleton.tsx', import.meta.url),
    'utf8',
  );

  assert.match(states, /Skeleton/);
  assert.match(states, /SkeletonLines/);
  assert.match(states, /aria-live="polite"/);
  assert.match(skeleton, /animate-pulse/);
  assert.doesNotMatch(states, /animate-spin|Spinner|Loader/);
});

test('shell provides a global smooth scroll-to-top control', async () => {
  const shell = await readFile(
    new URL('../src/components/Shell.tsx', import.meta.url),
    'utf8',
  );

  assert.match(shell, /ChevronUp/);
  assert.match(shell, /ScrollToTopButton/);
  assert.match(shell, /window\.scrollY > 320/);
  assert.match(shell, /window\.scrollTo\(\{ top: 0, behavior: 'smooth' \}\)/);
  assert.match(shell, /aria-label="Scroll to top"/);
  assert.match(shell, /fixed bottom-5 right-5/);
  assert.match(shell, /rounded-full/);
  assert.match(shell, /bg-emerald-400/);
});
