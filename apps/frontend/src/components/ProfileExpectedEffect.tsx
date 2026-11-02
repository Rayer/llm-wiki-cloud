'use client';

import { useState } from 'react';
import { useT } from '@/lib/i18n';

type Example = 'family' | 'quiet';

export function ProfileExpectedEffect() {
  const { t } = useT();
  const [example, setExample] = useState<Example>('family');
  const prefix = 'Profile.localUx.multiSource';

  return <section className="mt-4" aria-labelledby="profile-concept-comparison-heading">
    <div className="flex flex-wrap items-start justify-between gap-3">
      <div><h4 id="profile-concept-comparison-heading" className="text-sm font-semibold text-white">{t(`${prefix}.heading`)}</h4><p className="mt-1 text-xs leading-5 text-zinc-400">{t(`${prefix}.description`)}</p></div>
      <span className="rounded-full border border-amber-200/20 px-2.5 py-1 text-xs text-amber-100">{t(`${prefix}.fixed`)}</span>
    </div>
    <div role="tablist" aria-label={t(`${prefix}.tabs`)} className="mt-3 flex flex-wrap gap-2">
      {(['family', 'quiet'] as const).map((id) => <button key={id} type="button" role="tab" aria-selected={example === id} onClick={() => setExample(id)} className={`min-h-11 rounded px-3 text-sm ${example === id ? 'bg-emerald-300 text-zinc-950' : 'border border-white/10 text-zinc-300 hover:bg-white/5'}`}>{t(`${prefix}.${id}.needTitle`)}</button>)}
    </div>
    <p className="mt-3 rounded border border-white/10 bg-black/20 p-3 text-sm leading-6 text-zinc-200">{t(`${prefix}.${example}.need`)}</p>
    <article role="tabpanel" className="mt-3 rounded border border-emerald-200/20 bg-emerald-200/[0.03] p-4">
      <h5 className="text-sm font-semibold text-emerald-100">{t(`${prefix}.${example}.conceptTitle`)}</h5>
      <p className="mt-2 whitespace-pre-line text-sm leading-6 text-zinc-200">{t(`${prefix}.${example}.concept`)}</p>
      <p className="mt-2 text-xs leading-5 text-zinc-400">{t(`${prefix}.${example}.boundary`)}</p>
    </article>
    <details className="mt-3 rounded border border-white/10 p-3">
      <summary className="min-h-11 cursor-pointer py-2 text-sm text-zinc-300">{t(`${prefix}.sourcesHeading`)}</summary>
      <ul className="mt-2 space-y-2 text-sm leading-6 text-zinc-300">
        {(['s1', 's2', 's3'] as const).map((id) => <li key={id}><strong className="text-zinc-100">{t(`${prefix}.sources.${id}.title`)}</strong> — {t(`${prefix}.sources.${id}.text`)}</li>)}
      </ul>
    </details>
    <p className="mt-3 text-xs leading-5 text-zinc-500">{t(`${prefix}.note`)}</p>
  </section>;
}
