'use client';

import { useSyncExternalStore } from 'react';
import zhTW from '@/messages/zh-TW.json';
import en from '@/messages/en.json';

type Locale = 'zh-TW' | 'en';

const defaultLocale: Locale = 'zh-TW';
const localeStorageKey = 'locale';
const localeChangeEvent = 'locale-change';

function getLocale(): Locale {
  const stored = localStorage.getItem(localeStorageKey);
  return stored === 'en' || stored === 'zh-TW' ? stored : defaultLocale;
}

function subscribe(onStoreChange: () => void) {
  window.addEventListener('storage', onStoreChange);
  window.addEventListener(localeChangeEvent, onStoreChange);

  return () => {
    window.removeEventListener('storage', onStoreChange);
    window.removeEventListener(localeChangeEvent, onStoreChange);
  };
}

export function useT() {
  const locale = useSyncExternalStore(subscribe, getLocale, () => defaultLocale);

  const setLocale = (l: Locale) => {
    localStorage.setItem(localeStorageKey, l);
    window.dispatchEvent(new Event(localeChangeEvent));
  };

  const t = (key: string): string => {
    const parts = key.split('.');
    let obj: Record<string, unknown> = locale === 'zh-TW' ? zhTW : en;
    for (const part of parts) obj = (obj?.[part] ?? {}) as Record<string, unknown>;
    return typeof obj === 'string' ? obj : key;
  };

  return { locale, t, setLocale };
}
