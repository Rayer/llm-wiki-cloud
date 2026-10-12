'use client';

import { useT } from '@/lib/i18n';

export type ProfileExampleId = 'family' | 'parking';

const exampleIds: ProfileExampleId[] = ['family', 'parking'];

export function newProfileRequirementId(): string {
  return globalThis.crypto?.randomUUID?.() ?? `req-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

export function ProfileTemplates({
  disabled = false,
  onAddExample,
}: {
  disabled?: boolean;
  onAddExample: (exampleId: ProfileExampleId) => void;
}) {
  const { t } = useT();
  const prefix = 'Profile.localUx';

  return (
    <section className="mt-4 rounded-xl border border-white/10 bg-black/15 p-4" aria-labelledby="profile-examples-heading">
      <h4 id="profile-examples-heading" className="text-sm font-semibold text-white">{t(`${prefix}.examplesHeading`)}</h4>
      <p className="mt-1 text-xs leading-5 text-zinc-400">{t(`${prefix}.examplesDescription`)}</p>
      <div className="mt-3 grid gap-3 sm:grid-cols-2">
        {exampleIds.map((exampleId) => (
          <article key={exampleId} className="min-w-0 rounded-lg border border-white/10 p-3">
            <h5 className="text-sm font-medium text-zinc-100">{t(`${prefix}.examples.${exampleId}.title`)}</h5>
            <p className="mt-2 whitespace-pre-line break-words text-sm leading-6 text-zinc-300">
              {t(`${prefix}.examples.${exampleId}.text`)}
            </p>
            <button
              type="button"
              disabled={disabled}
              onClick={() => onAddExample(exampleId)}
              className="mt-3 min-h-11 rounded-lg border border-emerald-200/20 px-3 text-sm text-emerald-100 hover:bg-emerald-200/5 disabled:cursor-not-allowed disabled:opacity-50"
            >
              {t(`${prefix}.examples.${exampleId}.add`)}
            </button>
          </article>
        ))}
      </div>
      <p className="mt-3 text-xs leading-5 text-zinc-500">{t(`${prefix}.examplesNote`)}</p>
    </section>
  );
}
