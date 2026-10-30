'use client';

import { useT } from '@/lib/i18n';

export function LocaleControl() {
  const { t, locale, setLocale, hasFallback } = useT();
  return (
    <div className="mb-3 flex flex-wrap items-center justify-end gap-3">
      {hasFallback ? (
        <div role="status" className="flex flex-wrap items-center gap-2 text-xs text-amber-100">
          <span>{t('Shell.translationFallback')}</span>
          <button type="button" onClick={() => setLocale('en')} className="min-h-9 rounded-md border border-amber-200/20 px-2.5 text-amber-100 underline underline-offset-2 hover:bg-amber-100/10 focus-visible:outline focus-visible:outline-2 focus-visible:outline-amber-200">{t('Shell.useEnglish')}</button>
        </div>
      ) : null}
      <label className="flex min-h-9 items-center gap-2 text-xs text-zinc-400">
        <span>{t('Shell.language')}</span>
        <select aria-label={t('Shell.language')} value={locale} onChange={(event) => setLocale(event.target.value as typeof locale)} className="min-h-9 rounded-md border border-white/10 bg-zinc-900 px-2 text-zinc-200 focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-300">
          <option value="zh-TW">{t('Shell.localeTraditional')}</option>
          <option value="zh-CN">{t('Shell.localeSimplified')}</option>
          <option value="en">{t('Shell.localeEnglish')}</option>
        </select>
      </label>
    </div>
  );
}
