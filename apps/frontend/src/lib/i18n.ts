'use client';

import { useCallback, useMemo, useSyncExternalStore } from 'react';
import catalog from '@/generated/i18n/catalog';
import { translationMemoKey, type Locale } from './i18n-core';

type TranslationParams = Record<string, string | number>;
type LocaleToken = 'zh_tw' | 'zh' | 'en';

const localeStorageKey = 'locale';
const localeChangeEvent = 'locale-change';
const localeTokens: Record<Locale, LocaleToken> = { 'zh-TW': 'zh_tw', 'zh-CN': 'zh', en: 'en' };
const tokenLocales: Record<LocaleToken, Locale> = { zh_tw: 'zh-TW', zh: 'zh-CN', en: 'en' };
const defaultLocale: Locale = tokenLocales[catalog.default_locale as LocaleToken] ?? 'zh-TW';
const normalizedLocales: Record<string, Locale> = {
  'zh-TW': 'zh-TW', zh_tw: 'zh-TW', 'zh-Hant': 'zh-TW',
  'zh-CN': 'zh-CN', zh: 'zh-CN', zh_cn: 'zh-CN',
  en: 'en', 'en-US': 'en',
};

function getLocale(): Locale {
  try {
    return normalizedLocales[localStorage.getItem(localeStorageKey) ?? ''] ?? defaultLocale;
  } catch {
    return defaultLocale;
  }
}

function subscribe(onStoreChange: () => void) {
  const onStorage = (event: StorageEvent) => {
    if (event.key === localeStorageKey || event.key === null) onStoreChange();
  };
  window.addEventListener('storage', onStorage);
  window.addEventListener(localeChangeEvent, onStoreChange);
  return () => {
    window.removeEventListener('storage', onStorage);
    window.removeEventListener(localeChangeEvent, onStoreChange);
  };
}

function lookup(map: Record<string, unknown>, key: string): unknown {
  let node: unknown = map;
  for (const part of key.split('.')) {
    if (!node || typeof node !== 'object' || !(part in node)) return undefined;
    node = (node as Record<string, unknown>)[part];
  }
  return node;
}

function interpolate(text: string, params?: TranslationParams): string {
  if (!params) return text;
  return text.replace(/\{(\w+)\}/g, (match, name: string) =>
    params[name] === undefined ? match : String(params[name]),
  );
}

export function translate(locale: Locale, key: string, params?: TranslationParams): string {
  const token = localeTokens[locale];
  const localized = lookup(catalog.locales[token] as Record<string, unknown>, key);
  const base = lookup(catalog.base as Record<string, unknown>, key);
  if (typeof localized !== 'string' && typeof base !== 'string') {
    const message = `Missing i18n source key: ${key}`;
    if (process.env.NODE_ENV === 'test') throw new Error(message);
    if (process.env.NODE_ENV !== 'production') console.warn(message);
    const generic = lookup(catalog.base as Record<string, unknown>, 'I18n.missingKey');
    return typeof generic === 'string' ? generic : 'Text unavailable';
  }
  const value = typeof localized === 'string' ? localized : base as string;
  return interpolate(value, params);
}

export { translationMemoKey };
export const catalogRevision = catalog.bundle_revision;
export const catalogStatus = catalog.manifest.translation;

export function localeHasFallback(locale: Locale): boolean {
  return (catalog.manifest.translation.fallback_counts[localeTokens[locale]] ?? 0) > 0;
}

export function useT() {
  const locale = useSyncExternalStore(subscribe, getLocale, () => defaultLocale);
  const setLocale = useCallback((next: Locale) => {
    try {
      localStorage.setItem(localeStorageKey, next);
    } catch {
      // Translation still switches for this tab when storage is unavailable.
    }
    window.dispatchEvent(new Event(localeChangeEvent));
  }, []);

  const memoLocale = translationMemoKey(locale);
  const t = useMemo(
    () => (key: string, params?: TranslationParams) => translate(memoLocale, key, params),
    [memoLocale],
  );

  return { locale, t, setLocale, hasFallback: localeHasFallback(locale), catalogRevision };
}

export const useLocale = useT;
export type { Locale };
