export type Locale = 'zh-TW' | 'en';

/** The primitive dependency used to memoize translations for a locale. */
export function translationMemoKey(locale: Locale): Locale {
  return locale;
}
