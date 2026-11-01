import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

test('entry cards retain localized type badges and navigation without staggered motion', async () => {
  const entryCard = await readFile(
    new URL('../src/components/EntryCard.tsx', import.meta.url),
    'utf8',
  );
  const listClient = await readFile(
    new URL('../src/components/ListClient.tsx', import.meta.url),
    'utf8',
  );

  assert.doesNotMatch(entryCard, /animationDelay|animate-fade-in/);
  assert.match(entryCard, /<NavigationLink href=\{href\}/);
  assert.match(entryCard, /<Badge variant=\{entryType\}>/);
  assert.match(entryCard, /entryType === 'source' \? t\('Source\.singular'\) : t\('Entry\.singular'\)/);
  assert.match(listClient, /filtered\.map\(\(entry, index\) =>/);
  assert.match(listClient, /index=\{index\}/);
});

test('home entry and result cards preserve type distinctions without stagger delays', async () => {
  const homeClient = await readFile(
    new URL('../src/components/HomeClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(homeClient, /latestConcepts\.map\(\(concept, index\) =>/);
  assert.match(homeClient, /results\.map\(\(result, index\) =>/);
  assert.doesNotMatch(homeClient, /animationDelay/);
  const latestEntries = homeClient.slice(homeClient.indexOf('latestConcepts.map'), homeClient.indexOf('{results.map'));
  assert.doesNotMatch(latestEntries, /animate-fade-in/);
  assert.match(homeClient, /<Badge variant="concept">\{t\('Entry\.singular'\)\}/);
  assert.match(homeClient, /border-l-\[3px\]/);
  assert.match(homeClient, /type === 'source'[\s\S]*border-l-blue-400/);
  assert.match(homeClient, /type === 'concept'[\s\S]*border-l-emerald-400/);
});

test('loading state renders static skeleton cards with accessible loading announcements', async () => {
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
  assert.doesNotMatch(skeleton, /animate-pulse|animate-spin/);
  assert.match(skeleton, /aria-hidden="true"/);
  assert.doesNotMatch(states, /animate-spin|Spinner|Loader/);
});

test('shell provides a global smooth scroll-to-top control', async () => {
  const shell = await readFile(
    new URL('../src/components/Shell.tsx', import.meta.url),
    'utf8',
  );

  assert.match(shell, /ChevronUp/);
  assert.match(shell, /ScrollToTopButton/);
  assert.match(shell, /new IntersectionObserver\(/);
  assert.match(shell, /!entry\.isIntersecting && entry\.boundingClientRect\.top < 0/);
  assert.match(shell, /observer\.observe\(marker\.current\)/);
  assert.match(shell, /observer\.disconnect\(\)/);
  assert.match(shell, /top-\[320px\]/);
  assert.doesNotMatch(shell, /window\.scrollY|addEventListener\(['"]scroll['"]/);
  assert.match(shell, /window\.scrollTo\(\{ top: 0, behavior: 'smooth' \}\)/);
  assert.match(shell, /aria-label=\{t\('Shell\.scrollToTop'\)\}/);
  assert.match(shell, /fixed bottom-5 right-5/);
  assert.match(shell, /rounded-full/);
  assert.match(shell, /bg-emerald-400/);
});
