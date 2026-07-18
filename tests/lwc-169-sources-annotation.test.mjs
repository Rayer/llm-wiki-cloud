import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

import {
  ApiError,
  configureApiAuth,
  getRawFilePreview,
  getSourceAnnotation,
  getSources,
  updateSourceAnnotation,
} from '../src/lib/api.ts';
import { translationMemoKey } from '../src/lib/i18n-core.ts';
import { navigationDecision } from '../src/lib/navigation-decision.ts';
import { rawFileNameFromSource } from '../src/lib/raw-file-name.ts';
import { nextSourceDetailReloadVersion } from '../src/lib/source-detail.ts';
import {
  annotationClearDecision,
  annotationDirtyMetadata,
  annotationErrorKey,
  annotationLifecycleKey,
  annotationLoadDecision,
  annotationSaveDecision,
  createAnnotationRequestGate,
  isAnnotationDirty,
  normalizeAnnotationBody,
  normalizeAnnotationGeneration,
} from '../src/lib/source-annotation.ts';

function configureProject() {
  configureApiAuth({
    getAccessToken: () => 'jwt-token',
    refreshAccessToken: async () => null,
    onUnauthorized: () => undefined,
  });
  globalThis.window = { localStorage: { getItem: () => 'project-1' } };
}

function leafKeys(value, prefix = '') {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return [prefix];
  return Object.entries(value).flatMap(([key, child]) => leafKeys(child, prefix ? `${prefix}.${key}` : key));
}

test('translations are pure, memoized by locale, and locale files have exact key parity', async () => {
  assert.equal(translationMemoKey('en'), translationMemoKey('en'));
  assert.notEqual(translationMemoKey('en'), translationMemoKey('zh-TW'));

  const [en, zhTW] = await Promise.all([
    readFile(new URL('../src/messages/en.json', import.meta.url), 'utf8').then(JSON.parse),
    readFile(new URL('../src/messages/zh-TW.json', import.meta.url), 'utf8').then(JSON.parse),
  ]);
  assert.deepEqual(leafKeys(en).sort(), leafKeys(zhTW).sort());
});

test('source lifecycle union preserves pending raw paths without fabricating ids', async () => {
  configureProject();
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => Response.json({ sources: [
    { raw_path: 'raw/pending.md', lifecycle_status: 'new', has_annotation: false },
    { id: 'source-1', slug: 'compiled', raw_path: 'raw/compiled.md', lifecycle_status: 'notes_pending', annotation_present: true, annotation_allowed: true },
    { id: 'source-2', slug: 'changed', raw_path: 'raw/changed.md', lifecycle_status: 'content_pending' },
    { id: 'source-3', slug: 'broken', raw_path: 'raw/broken.md', lifecycle_status: 'error' },
    { id: 'source-4', slug: 'current', raw_path: 'raw/current.md', lifecycle_status: 'synced' },
  ] });
  try {
    const [pending, compiled, changed, broken, current] = await getSources();
    assert.equal(pending.id, undefined);
    assert.equal(pending.rawPath, 'raw/pending.md');
    assert.equal(pending.lifecycle, 'new');
    assert.equal(compiled.id, 'source-1');
    assert.equal(compiled.lifecycle, 'notes_pending');
    assert.equal(compiled.annotationPresent, true);
    assert.equal(changed.lifecycle, 'content_pending');
    assert.equal(broken.lifecycle, 'error');
    assert.equal(current.lifecycle, 'synced');
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('raw preview converts lifecycle raw paths to the basename URL while preserving the display path', async () => {
  configureProject();
  assert.equal(rawFileNameFromSource('raw/foo.md'), 'foo.md');
  assert.equal(rawFileNameFromSource('raw/folder/foo.md'), 'foo.md');
  const originalFetch = globalThis.fetch;
  let requestedUrl = '';
  globalThis.fetch = async (url) => {
    requestedUrl = String(url);
    return new Response('# Raw\n');
  };
  try {
    await getRawFilePreview('raw/foo.md');
    assert.match(requestedUrl, /\/api\/v1\/raw\/foo\.md\?preview=true$/);
    assert.doesNotMatch(requestedUrl, /raw%2Ffoo\.md/);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('annotation GET and PUT normalize body, generation, and lifecycle metadata', async () => {
  configureProject();
  const originalFetch = globalThis.fetch;
  const calls = [];
  globalThis.fetch = async (url, init) => {
    calls.push([String(url), init]);
    if (init?.method === 'PUT') {
      return Response.json({
        annotation: 'saved', expected_generation: 8, has_annotation: true,
        ann_sha256: 'ann-sha', annotation_dirty: true, raw_dirty: false, dirty: true,
        lifecycle_status: 'notes_pending', updated_at: '2026-07-18T10:00:00Z', updated_by: 'editor@example.test',
      });
    }
    return Response.json({
      body: 'loaded', expected_generation: '7', has_annotation: true,
      ann_sha256: 'before', annotation_dirty: false, raw_dirty: true, dirty: true,
      lifecycle_status: 'content_pending', updated_at: '2026-07-18T09:00:00Z', updated_by: 'author@example.test',
    });
  };
  try {
    assert.deepEqual(await getSourceAnnotation('source 1'), {
      body: 'loaded', expectedGeneration: '7', hasAnnotation: true, annSha256: 'before',
      annotationDirty: false, rawDirty: true, dirty: true, lifecycleStatus: 'content_pending',
      updatedAt: '2026-07-18T09:00:00Z', updatedBy: 'author@example.test',
    });
    const saved = await updateSourceAnnotation('source 1', ' saved\r\n', '7');
    assert.equal(saved.expectedGeneration, '8');
    assert.equal(saved.body, 'saved');
    assert.deepEqual(JSON.parse(calls[1][1].body), { body: 'saved', expected_generation: '7' });
    assert.equal(calls[1][1].headers['X-Project-ID'], 'project-1');
    assert.equal(calls[1][1].headers.Authorization, 'Bearer jwt-token');
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('annotation state decisions cover normalized no-op, clear, demo, conflict, and metadata behavior', () => {
  assert.equal(normalizeAnnotationBody(' a\r\n b \n'), 'a\n b');
  assert.equal(normalizeAnnotationGeneration(8), '8');
  assert.equal(normalizeAnnotationGeneration('  '), null);
  assert.equal(isAnnotationDirty('same\n', 'same\r\n'), false);
  assert.equal(annotationSaveDecision({ body: 'same', draft: ' same\n', saving: false }), 'noop');
  assert.equal(annotationSaveDecision({ body: 'same', draft: 'changed', saving: false }), 'save');
  assert.equal(annotationSaveDecision({ body: 'same', draft: 'changed', saving: true }), 'noop');
  assert.equal(annotationClearDecision('  '), 'noop');
  assert.equal(annotationClearDecision('text'), 'save');
  assert.equal(annotationLoadDecision(true), 'skip');
  assert.equal(annotationLoadDecision(false), 'load');
  assert.equal(annotationErrorKey(412), null);
  assert.equal(annotationErrorKey(409), 'locked');
  assert.equal(annotationLifecycleKey('notes_pending'), 'Source.lifecycle.notes_pending');
  assert.equal(annotationLifecycleKey('other'), null);
  assert.deepEqual(annotationDirtyMetadata({ dirty: true, annotationDirty: false, rawDirty: true }), ['dirtyOverall', 'dirtyRaw']);
  assert.equal(nextSourceDetailReloadVersion(4), 5);
});

test('annotation request gate ignores deferred source A completion after source B becomes current', async () => {
  const gate = createAnnotationRequestGate();
  const deferred = () => {
    let resolve;
    return { promise: new Promise((done) => { resolve = done; }), resolve };
  };
  const a = deferred();
  const b = deferred();
  const editor = { source: '', loading: false, saving: false, conflict: false, disabled: false };

  const load = async (source, response) => {
    const request = gate.begin();
    editor.loading = true;
    editor.saving = false;
    editor.conflict = false;
    editor.disabled = false;
    try {
      const value = await response;
      if (!gate.isCurrent(request)) return;
      editor.source = value;
    } finally {
      if (gate.isCurrent(request)) editor.loading = false;
    }
  };

  const loadA = load('A', a.promise);
  const loadB = load('B', b.promise);
  b.resolve('B');
  await loadB;
  a.resolve('A');
  await loadA;

  assert.deepEqual(editor, { source: 'B', loading: false, saving: false, conflict: false, disabled: false });
});

test('a 412 remains a conflict signal and never becomes an automatic overwrite', async () => {
  configureProject();
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => new Response(null, { status: 412 });
  try {
    await assert.rejects(() => updateSourceAnnotation('source-1', 'x', '1'), (error) => error instanceof ApiError && error.status === 412);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('navigation decisions guard only ordinary same-tab navigation and leave browser unload semantics intact', () => {
  assert.equal(navigationDecision({ blocked: true }), 'confirm');
  assert.equal(navigationDecision({ blocked: false }), 'allow');
  assert.equal(navigationDecision({ blocked: true, modified: true }), 'allow');
  assert.equal(navigationDecision({ blocked: true, button: 1 }), 'allow');
  assert.equal(navigationDecision({ blocked: true, target: '_blank' }), 'allow');
  assert.equal(navigationDecision({ blocked: true, download: true }), 'allow');
});

test('UI wiring uses provider capture plus guarded programmatic and component navigation paths', async () => {
  const files = await Promise.all([
    'src/components/NavigationBlocker.tsx',
    'src/components/SourceAnnotationEditor.tsx',
    'src/components/DetailClient.tsx',
    'src/components/SourceListClient.tsx',
    'src/components/WorkspaceProvider.tsx',
    'src/components/NewProjectModal.tsx',
    'src/components/ui/CommandPalette.tsx',
    'src/components/MarkdownView.tsx',
    'src/components/EntryCard.tsx',
    'src/components/HomeClient.tsx',
  ].map(async (path) => [path, await readFile(new URL(`../${path}`, import.meta.url), 'utf8')]));
  const source = Object.fromEntries(files);
  assert.match(source['src/components/NavigationBlocker.tsx'], /onClickCapture/);
  assert.match(source['src/components/NavigationBlocker.tsx'], /beforeunload/);
  assert.match(source['src/components/NavigationBlocker.tsx'], /navigationDecision/);
  assert.doesNotMatch(source['src/components/NavigationBlocker.tsx'], /history\.forward|router\.push\s*=/);
  for (const path of ['src/components/MarkdownView.tsx', 'src/components/EntryCard.tsx', 'src/components/HomeClient.tsx']) {
    assert.match(source[path], /NavigationLink/);
  }
  for (const path of ['src/components/WorkspaceProvider.tsx', 'src/components/NewProjectModal.tsx', 'src/components/ui/CommandPalette.tsx']) {
    assert.match(source[path], /confirmNavigation/);
  }
  assert.match(source['src/components/SourceListClient.tsx'], /rawFileNameFromSource\(rawPath\)/);
  assert.match(source['src/components/SourceListClient.tsx'], /aria-labelledby="source-raw-preview-title"/);
  assert.match(source['src/components/SourceAnnotationEditor.tsx'], /annotationLoadDecision\(isDemoSession\) === 'skip'/);
  assert.match(source['src/components/SourceAnnotationEditor.tsx'], /createAnnotationRequestGate/);
  assert.match(source['src/components/SourceAnnotationEditor.tsx'], /requestGate\.current\.isCurrent\(request\)/);
  assert.match(source['src/components/SourceAnnotationEditor.tsx'], /gate\.invalidate\(\)/);
  assert.doesNotMatch(source['src/components/SourceAnnotationEditor.tsx'], /annotation\.updatedBy/);
  assert.match(source['src/components/SourceAnnotationEditor.tsx'], /onSaved\?\.\(\)/);
  assert.doesNotMatch(source['src/components/SourceAnnotationEditor.tsx'], /router\.refresh|triggerPipeline/);
  assert.match(source['src/components/DetailClient.tsx'], /onSaved=\{reloadEntry\}/);
});
