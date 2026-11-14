'use client';

import { useEffect, useState, type ReactNode } from 'react';
import { getReleaseNote, getReleaseNotesIndex, type ReleaseNoteDocument, type ReleaseNotesIndex } from '@/lib/api';
import { useT } from '@/lib/i18n';

const productVersionPattern = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/;

export function ReleaseNotesReader({
  presentation,
  initialVersion,
  showBuildInformation = false,
  buildInformation,
}: {
  presentation: 'dialog' | 'page';
  initialVersion: string;
  showBuildInformation?: boolean;
  buildInformation?: ReactNode;
}) {
  const { locale, t } = useT();
  const [selectedVersion, setSelectedVersion] = useState(initialVersion || 'current');
  const [activeTab, setActiveTab] = useState<'notes' | 'build'>('notes');
  const [retry, setRetry] = useState(0);
  const requestKey = `${selectedVersion}\u0000${locale}\u0000${retry}`;
  const [loaded, setLoaded] = useState<{
    requestKey: string;
    index: ReleaseNotesIndex | null;
    document: ReleaseNoteDocument | null;
    failed: boolean;
  }>({ requestKey: '', index: null, document: null, failed: false });
  const requestIsLoaded = loaded.requestKey === requestKey;
  const invalidSelectedVersion = selectedVersion !== 'current' && !productVersionPattern.test(selectedVersion);
  const loading = !invalidSelectedVersion && !requestIsLoaded;
  const index = requestIsLoaded ? loaded.index : null;
  const document = requestIsLoaded ? loaded.document : null;
  const failed = requestIsLoaded && loaded.failed;

  useEffect(() => {
    let active = true;
    if (invalidSelectedVersion) {
      return () => { active = false; };
    }
    getReleaseNotesIndex().then(async (loadedIndex) => {
      if (!active) return;
      const resolvedVersion = selectedVersion === 'current'
        ? loadedIndex.current_status === 'available' ? loadedIndex.current_product_version ?? '' : ''
        : selectedVersion;
      if (!resolvedVersion) {
        setLoaded({ requestKey, index: loadedIndex, document: null, failed: false });
        return;
      }
      let loadedDocument: ReleaseNoteDocument | null = null;
      let loadFailed = false;
      try {
        loadedDocument = await getReleaseNote(resolvedVersion, locale === 'en' ? 'en' : 'zh-TW');
      } catch {
        loadFailed = true;
      }
      if (active) setLoaded({ requestKey, index: loadedIndex, document: loadedDocument, failed: loadFailed });
    }).catch(async () => {
      if (!active) return;
      let loadedDocument: ReleaseNoteDocument | null = null;
      let loadFailed = true;
      if (selectedVersion !== 'current' && productVersionPattern.test(selectedVersion)) {
        try {
          loadedDocument = await getReleaseNote(selectedVersion, locale === 'en' ? 'en' : 'zh-TW');
          loadFailed = false;
        } catch { /* Keep the exact requested version unavailable. */ }
      }
      if (active) {
        setLoaded({ requestKey, index: null, document: loadedDocument, failed: loadFailed });
      }
    });
    return () => { active = false; };
  }, [requestKey, selectedVersion, locale, retry, invalidSelectedVersion]);

  const selectedIsCurrentRequest = selectedVersion === 'current';
  const currentStatus = index?.current_status ?? 'unavailable';
  const selectedMissing = selectedIsCurrentRequest && currentStatus === 'missing';
  const selectedUnavailable = selectedIsCurrentRequest && currentStatus === 'unavailable';
  const heading = presentation === 'dialog' ? t('Shell.versionInfo') : t('ReleaseNotes.pageTitle');

  return (
    <div className="release-notes-reader">
      {presentation === 'dialog' ? (
        <>
          <h2 id="build-info-title" className="pr-10 text-lg font-semibold text-[#F0F3F1]">{heading}</h2>
          {showBuildInformation ? (
            <div role="tablist" aria-label={t('ReleaseNotes.readerTabs')} className="mt-4 flex gap-2 border-b border-white/10">
              <button role="tab" id="release-notes-tab" aria-selected={activeTab === 'notes'} aria-controls="release-notes-panel" type="button" onClick={() => setActiveTab('notes')} className="min-h-11 border-b-2 px-3 text-sm" data-active={activeTab === 'notes'}>
                {t('ReleaseNotes.notesTab')}
              </button>
              <button role="tab" id="release-build-tab" aria-selected={activeTab === 'build'} aria-controls="release-build-panel" type="button" onClick={() => setActiveTab('build')} className="min-h-11 border-b-2 px-3 text-sm" data-active={activeTab === 'build'}>
                {t('ReleaseNotes.buildTab')}
              </button>
            </div>
          ) : null}
          {activeTab === 'build' && showBuildInformation ? (
            <div role="tabpanel" id="release-build-panel" aria-labelledby="release-build-tab" className="mt-4">{buildInformation}</div>
          ) : (
            <div role="tabpanel" id="release-notes-panel" aria-labelledby={showBuildInformation ? 'release-notes-tab' : undefined} className="mt-4">
              {renderNotesContent()}
            </div>
          )}
        </>
      ) : (
        <>
          <h1 className="text-2xl font-semibold text-white">{heading}</h1>
          {renderNotesContent()}
        </>
      )}
    </div>
  );

  function renderNotesContent() {
    const currentVersion = index?.current_product_version ?? index?.runtime_product_version ?? '';
    const history = index?.history ?? [];
    const options = new Map<string, string>();
    if (index?.current_status === 'available' && index.current_product_version) {
      options.set(index.current_product_version, `${t('ReleaseNotes.current')} · ${index.current_product_version}`);
    }
    for (const item of history) {
      options.set(item.product_version, `${item.product_version} · ${formatDate(item.published_at)}`);
    }

    return (
      <div>
        <div className="mt-3 flex flex-wrap items-center gap-3">
          <label htmlFor="release-notes-version" className="text-sm text-zinc-300">{t('ReleaseNotes.version')}</label>
          <select id="release-notes-version" value={selectedVersion} onChange={(event) => setSelectedVersion(event.target.value)} className="min-h-11 max-w-full rounded border border-white/15 bg-[#202324] px-3 text-sm text-white focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-400">
            <option value="current">{t('ReleaseNotes.current')} · {currentVersion || t('ReleaseNotes.currentRuntime')}</option>
            {[...options].map(([version, label]) => <option key={version} value={version}>{label}</option>)}
            {selectedVersion !== 'current' && !options.has(selectedVersion) ? <option value={selectedVersion}>{selectedVersion}</option> : null}
          </select>
        </div>

        {loading ? <p role="status" className="mt-5 text-sm text-zinc-400">{t('ReleaseNotes.loading')}</p> : null}
        {invalidSelectedVersion ? <p role="alert" className="mt-5 text-sm text-amber-100">{t('ReleaseNotes.invalidVersion')}</p> : null}
        {!loading && failed ? (
          <div role="alert" className="mt-5 rounded border border-amber-300/30 bg-amber-400/8 p-4 text-sm text-amber-100">
            <p>{t('ReleaseNotes.unavailable')}</p>
            <button type="button" onClick={() => setRetry((value) => value + 1)} className="mt-3 min-h-11 rounded border border-amber-200/40 px-3">{t('ReleaseNotes.retry')}</button>
          </div>
        ) : null}
        {!loading && !failed && selectedMissing ? <p role="status" className="mt-5 text-sm text-zinc-300">{t('ReleaseNotes.missingCurrent', { version: currentVersion })}</p> : null}
        {!loading && !failed && selectedUnavailable ? (
          <div role="status" className="mt-5 rounded border border-amber-300/30 bg-amber-400/8 p-4 text-sm text-amber-100">
            <p>{t('ReleaseNotes.unavailable')}</p>
            <button type="button" onClick={() => setRetry((value) => value + 1)} className="mt-3 min-h-11 rounded border border-amber-200/40 px-3">{t('ReleaseNotes.retry')}</button>
          </div>
        ) : null}
        {!invalidSelectedVersion && !loading && !failed && !selectedMissing && !selectedUnavailable && !document ? <p role="status" className="mt-5 text-sm text-zinc-400">{t('ReleaseNotes.notFound')}</p> : null}
        {document ? (
          <article className="mt-5 space-y-5 text-sm text-zinc-200">
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-zinc-400">
              <span>{document.is_current ? t('ReleaseNotes.current') : t('ReleaseNotes.historical')} · {document.product_version}</span>
              <time dateTime={document.published_at}>{t('ReleaseNotes.publishedAt', { date: formatDate(document.published_at) })}</time>
              {document.locale_fallback ? <span role="note">{t('ReleaseNotes.englishFallback')}</span> : null}
            </div>
            {document.entries.map((entry, index) => (
              <section key={`${entry.title}-${index}`} className="border-t border-white/10 pt-4">
                <h2 className="text-base font-semibold text-white">{entry.title}</h2>
                <dl className="mt-3 space-y-3 leading-6">
                  <div><dt className="font-medium text-zinc-300">{t('ReleaseNotes.whatChanged')}</dt><dd>{entry.what_changed}</dd></div>
                  <div><dt className="font-medium text-zinc-300">{t('ReleaseNotes.userBenefit')}</dt><dd>{entry.user_benefit}</dd></div>
                  <div><dt className="font-medium text-zinc-300">{t('ReleaseNotes.whereToUse')}</dt><dd>{entry.where_to_use}</dd></div>
                </dl>
                {entry.limitations.length ? <div className="mt-3"><h3 className="font-medium text-zinc-300">{t('ReleaseNotes.limitations')}</h3><ul className="mt-1 list-disc space-y-1 pl-5">{entry.limitations.map((limitation, limitationIndex) => <li key={limitationIndex}>{limitation}</li>)}</ul></div> : null}
              </section>
            ))}
          </article>
        ) : null}
      </div>
    );
  }
}

function formatDate(value: string) {
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return value;
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeZone: 'UTC' }).format(date);
}
