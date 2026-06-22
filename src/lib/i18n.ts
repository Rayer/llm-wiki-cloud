'use client';

import { useState, useEffect } from 'react';
import zhTW from '@/messages/zh-TW.json';
import en from '@/messages/en.json';

type Locale = 'zh-TW' | 'en';

export function useT() {
  const [locale, setLocaleState] = useState<Locale>('zh-TW');

  useEffect(() => {
    const stored = localStorage.getItem('locale') as Locale;
    if (stored === 'zh-TW' || stored === 'en') setLocaleState(stored);
  }, []);

  const setLocale = (l: Locale) => {
    localStorage.setItem('locale', l);
    setLocaleState(l);
  };

  const t = (key: string): string => {
    const parts = key.split('.');
    let obj: Record<string, unknown> = locale === 'zh-TW' ? zhTW : en;
    for (const part of parts) obj = (obj?.[part] ?? {}) as Record<string, unknown>;
    return typeof obj === 'string' ? obj : key;
  };

  return { locale, t, setLocale };
}
