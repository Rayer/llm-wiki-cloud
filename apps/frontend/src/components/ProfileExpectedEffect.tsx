import { useT } from '@/lib/i18n';

const tags = ['restaurant', 'district', 'homeStyle', 'parking', 'childSeat', 'narrowWalkway', 'strollerUnknown'] as const;

export function ProfileExpectedEffect() {
  const { t } = useT();
  const prefix = 'Profile.localUx.guide';

  return (
    <section className="mt-4" aria-labelledby="profile-concept-comparison-heading">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h4 id="profile-concept-comparison-heading" className="text-sm font-semibold text-white">
          {t(`${prefix}.conceptComparisonHeading`)}
        </h4>
        <span className="rounded-full border border-amber-200/20 px-2.5 py-1 text-xs text-amber-100">
          {t(`${prefix}.staticExampleLabel`)}
        </span>
      </div>
      <p className="mt-1 text-xs leading-5 text-zinc-400">{t(`${prefix}.conceptComparisonDescription`)}</p>
      <div className="mt-3 grid min-w-0 gap-3 sm:grid-cols-2">
        <article className="min-w-0 rounded-xl border border-white/10 bg-black/20 p-4" aria-labelledby="profile-concept-without-heading">
          <h5 id="profile-concept-without-heading" className="text-sm font-semibold text-zinc-100">
            {t(`${prefix}.withoutProfile`)}
          </h5>
          <p className="mt-3 text-sm font-medium text-zinc-200">{t(`${prefix}.withoutTitle`)}</p>
          <p className="mt-2 whitespace-pre-line text-sm leading-6 text-zinc-300">{t(`${prefix}.withoutBody`)}</p>
          <p className="mt-3 text-xs font-medium text-zinc-400">{t(`${prefix}.generalTagsHeading`)}</p>
          <ul className="mt-2 flex flex-wrap gap-2" aria-label={t(`${prefix}.generalTagsHeading`)}>
            {tags.slice(0, 3).map((tag) => (
              <li key={tag} className="max-w-full break-words rounded-md border border-white/15 px-2 py-1 text-xs text-zinc-300">
                {t(`${prefix}.tags.${tag}`)}
              </li>
            ))}
          </ul>
        </article>
        <article className="min-w-0 rounded-xl border border-emerald-200/20 bg-emerald-200/[0.03] p-4" aria-labelledby="profile-concept-with-heading">
          <h5 id="profile-concept-with-heading" className="text-sm font-semibold text-emerald-100">
            {t(`${prefix}.withProfile`)}
          </h5>
          <p className="mt-3 text-sm font-medium text-zinc-200">{t(`${prefix}.withTitle`)}</p>
          <p className="mt-2 whitespace-pre-line text-sm leading-6 text-zinc-300">{t(`${prefix}.withBody`)}</p>
          <p className="mt-3 text-xs font-medium text-zinc-400">{t(`${prefix}.preferenceTagsHeading`)}</p>
          <ul className="mt-2 flex flex-wrap gap-2" aria-label={t(`${prefix}.preferenceTagsHeading`)}>
            {tags.map((tag, index) => (
              <li key={tag} className={`max-w-full break-words rounded-md border px-2 py-1 text-xs ${index < 3 ? 'border-white/15 text-zinc-300' : index === tags.length - 1 ? 'border-dashed border-white/20 text-zinc-300' : 'border-emerald-200/20 bg-emerald-200/[0.04] text-emerald-100'}`}>
                {t(`${prefix}.tags.${tag}`)}
              </li>
            ))}
          </ul>
        </article>
      </div>
      <p className="mt-3 text-xs leading-5 text-zinc-400">{t(`${prefix}.unknownBoundary`)}</p>
    </section>
  );
}
