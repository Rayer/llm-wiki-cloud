export type Locale = 'zh-TW' | 'zh-CN' | 'en';
export type LocaleToken = 'zh_tw' | 'zh' | 'en';

export type LocaleOption = { token: LocaleToken; locale: Locale; label: string };

const localeByToken: Record<LocaleToken, Locale> = { zh_tw: 'zh-TW', zh: 'zh-CN', en: 'en' };
const tokenByPreference: Record<string, LocaleToken> = {
  'zh-TW': 'zh_tw', zh_tw: 'zh_tw', 'zh-Hant': 'zh_tw',
  'zh-CN': 'zh', zh: 'zh', zh_cn: 'zh',
  en: 'en', 'en-US': 'en',
};

export function configuredLocaleOptions(
  targetLocales: readonly string[],
  metadata: readonly { token: string; label: string }[],
): LocaleOption[] {
  return metadata.flatMap(({ token, label }) => {
    if (!['zh_tw', 'zh', 'en'].includes(token) || !targetLocales.includes(token)) return [];
    return [{ token: token as LocaleToken, locale: localeByToken[token as LocaleToken], label }];
  });
}

export function normalizeLocalePreference(
  preference: string | null | undefined,
  targetLocales: readonly string[],
  defaultToken: string,
): Locale {
  const defaultLocale = localeByToken[defaultToken as LocaleToken];
  const fallback = targetLocales.includes(defaultToken) && defaultLocale
    ? defaultLocale
    : targetLocales.map((token) => localeByToken[token as LocaleToken]).find(Boolean) ?? 'zh-TW';
  const token = tokenByPreference[preference ?? ''];
  return token && targetLocales.includes(token) ? localeByToken[token] : fallback;
}

/** The primitive dependency used to memoize translations for a locale. */
export function translationMemoKey(locale: Locale): Locale {
  return locale;
}
