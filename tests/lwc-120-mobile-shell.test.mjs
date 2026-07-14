import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

test('shell exposes a mobile navigation drawer while preserving desktop sidebar navigation', async () => {
  const shell = await readFile(
    new URL('../src/components/Shell.tsx', import.meta.url),
    'utf8',
  );

  assert.match(shell, /import \{[^}]*Menu[^}]*X[^}]*\} from 'lucide-react';/s);
  assert.match(shell, /const \[mobileNavOpen, setMobileNavOpen\] = useState\(false\);/);
  assert.match(shell, /aria-controls="mobile-navigation"/);
  assert.match(shell, /aria-expanded=\{mobileNavOpen\}/);
  assert.match(shell, /aria-label=\{mobileNavOpen \? t\('Shell.closeNavigation'\) : t\('Shell.openNavigation'\)\}/);
  assert.match(shell, /id="mobile-navigation"/);
  assert.match(shell, /translate-x-0[\s\S]*-translate-x-full/);
  assert.match(shell, /lg:sticky[\s\S]*lg:translate-x-0/);
  assert.match(shell, /event\.key === 'Escape'/);
  assert.match(shell, /setMobileNavOpen\(false\);[\s\S]*pathname/);
  assert.match(shell, /<nav[\s\S]*aria-label=\{t\('Shell.navigation'\)\}/);
});

test('home search controls keep mobile touch targets at least 44px tall', async () => {
  const homeClient = await readFile(
    new URL('../src/components/HomeClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(homeClient, /<input[\s\S]*className="[^"]*min-h-12/s);
  assert.match(homeClient, /type="submit"[\s\S]*className="[^"]*min-h-12/s);
  assert.doesNotMatch(homeClient, /type="submit"[\s\S]*className="[^"]*min-h-10/s);
});
