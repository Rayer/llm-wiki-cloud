'use client';

import catalog from '@/generated/i18n/catalog';
import { configuredLocaleOptions } from '@/lib/i18n-core';
import { useT } from '@/lib/i18n';

const localeOptions = configuredLocaleOptions(catalog.target_locales, catalog.locale_metadata);

export function LocaleControl() {
  const { t, locale, setLocale, hasFallback } = useT();
  const hasEnglish = localeOptions.some(({ locale: option }) => option === 'en');
  return (
    <div className="mb-3 flex flex-wrap items-center justify-end gap-3">
      {hasFallback && hasEnglish ? (
        <div role="status" className="flex flex-wrap items-center gap-2 text-xs text-amber-100">
          <span>{t('Shell.translationFallback')}</span>
          <button type="button" onClick={() => setLocale('en')} className="min-h-9 rounded-md border border-amber-200/20 px-2.5 text-amber-100 underline underline-offset-2 hover:bg-amber-100/10 focus-visible:outline focus-visible:outline-2 focus-visible:outline-amber-200">{t('Shell.useEnglish')}</button>
        </div>
      ) : null}
      <label className="flex min-h-9 items-center gap-2 text-xs text-zinc-400">
        <span>{t('Shell.language')}</span>
        <select aria-label={t('Shell.language')} value={locale} onChange={(event) => setLocale(event.target.value as typeof locale)} className="min-h-9 rounded-md border border-white/10 bg-zinc-900 px-2 text-zinc-200 focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-300">
          {localeOptions.map(({ locale: option, label }) => <option key={option} value={option}>{label}</option>)}
        </select>
      </label>
    </div>
  );
}
