'use client';

import { useCallback, useMemo, useSyncExternalStore } from 'react';
import catalog from '@/generated/i18n/catalog';
import { configuredLocaleOptions, normalizeLocalePreference, translationMemoKey, type Locale, type LocaleToken } from './i18n-core';

type TranslationParams = Record<string, string | number>;
const localeStorageKey = 'locale';
const localeChangeEvent = 'locale-change';
const localeTokens: Record<Locale, LocaleToken> = { 'zh-TW': 'zh_tw', 'zh-CN': 'zh', en: 'en' };
const localeOptions = configuredLocaleOptions(catalog.target_locales, catalog.locale_metadata);
const targetLocales = localeOptions.map(({ token }) => token);
const defaultLocale = normalizeLocalePreference(undefined, targetLocales, catalog.default_locale);
let memoryLocale: Locale | null = null;

function getLocale(): Locale {
  if (memoryLocale) return memoryLocale;
  try {
    return normalizeLocalePreference(localStorage.getItem(localeStorageKey), targetLocales, catalog.default_locale);
  } catch {
    return defaultLocale;
  }
}

function subscribe(onStoreChange: () => void) {
  const onStorage = (event: StorageEvent) => {
    if (event.key === localeStorageKey || event.key === null) {
      memoryLocale = null;
      onStoreChange();
    }
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

function lookupBase(map: Record<string, unknown>, key: string): unknown {
  const flatValue = map[key];
  return typeof flatValue === 'string' ? flatValue : lookup(map, key);
}

function interpolate(text: string, params?: TranslationParams): string {
  if (!params) return text;
  return text.replace(/\{(\w+)\}/g, (match, name: string) =>
    params[name] === undefined ? match : String(params[name]),
  );
}

export function translate(locale: Locale, key: string, params?: TranslationParams): string {
  return translateFromCatalog(catalog as unknown as TranslationCatalog, locale, key, params);
}

type TranslationCatalog = { base: Record<string, unknown>; locales: Record<string, unknown> };

export function translateFromCatalog(source: TranslationCatalog, locale: Locale, key: string, params?: TranslationParams): string {
  const token = localeTokens[locale];
  const localized = lookup(source.locales[token] as Record<string, unknown>, key);
  const base = lookupBase(source.base, key);
  if (typeof localized !== 'string' && typeof base !== 'string') {
    const message = `Missing i18n source key: ${key}`;
    if (process.env.NODE_ENV === 'test') throw new Error(message);
    if (process.env.NODE_ENV !== 'production') console.warn(message);
    const generic = lookupBase(source.base, 'I18n.missingKey');
    return typeof generic === 'string' ? generic : 'Text unavailable';
  }
  const value = typeof localized === 'string' ? localized : base as string;
  return interpolate(value, params);
}

export { translationMemoKey };
export const catalogRevision = catalog.bundle_revision;
export const catalogStatus = catalog.manifest.translation;

export function localeHasFallback(locale: Locale): boolean {
  return targetLocales.includes(localeTokens[locale]) && (catalog.manifest.translation.fallback_counts[localeTokens[locale]] ?? 0) > 0;
}

export function useT() {
  const locale = useSyncExternalStore(subscribe, getLocale, () => defaultLocale);
  const setLocale = useCallback((next: Locale) => {
    const selected = targetLocales.includes(localeTokens[next]) ? next : defaultLocale;
    try {
      localStorage.setItem(localeStorageKey, selected);
      memoryLocale = null;
    } catch {
      memoryLocale = selected;
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
