import type { ProfileRequirement } from '@/lib/api';
import { useT } from '@/lib/i18n';
import type { ProfileTemplateId } from './ProfileTemplates';

export function ProfileExpectedEffect({
  selectedTemplateId,
  requirements,
}: {
  selectedTemplateId: ProfileTemplateId | null;
  requirements: ProfileRequirement[];
}) {
  const { t } = useT();
  const prefix = 'Profile.localUx';

  return (
    <section className="mt-4 rounded-xl border border-sky-300/20 bg-sky-300/5 p-4" aria-labelledby="profile-effect-heading">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h4 id="profile-effect-heading" className="text-sm font-semibold text-white">{t(`${prefix}.expectedHeading`)}</h4>
          <p className="mt-1 text-xs leading-5 text-zinc-400">{t(`${prefix}.expectedDescription`)}</p>
        </div>
        <span className="rounded-full border border-sky-200/20 px-2.5 py-1 text-xs text-sky-100">{t(`${prefix}.staticExampleLabel`)}</span>
      </div>
      {selectedTemplateId ? (
        <div className="mt-3 rounded-lg border border-white/10 bg-black/20 p-3">
          <p className="text-xs font-medium text-sky-100">{t(`${prefix}.templates.${selectedTemplateId}.impact`)}</p>
          <p className="mt-2 whitespace-pre-line text-sm leading-6 text-zinc-200">{t(`${prefix}.templates.${selectedTemplateId}.example`)}</p>
        </div>
      ) : (
        <p className="mt-3 text-sm leading-6 text-zinc-400">{t(`${prefix}.noSelectedExample`)}</p>
      )}
      <div className="mt-3 border-t border-white/10 pt-3">
        <h5 className="text-xs font-semibold uppercase tracking-wide text-zinc-300">{t(`${prefix}.draftSummaryHeading`)}</h5>
        {requirements.length > 0 ? (
          <ol className="mt-2 list-decimal space-y-1 pl-5 text-sm leading-6 text-zinc-300">
            {requirements.map((requirement) => <li key={requirement.id} className="break-words">{requirement.text || t(`${prefix}.blankRequirement`)}</li>)}
          </ol>
        ) : (
          <p className="mt-2 text-sm text-zinc-400">{t(`${prefix}.generalDraftSummary`)}</p>
        )}
        <p className="mt-2 text-xs leading-5 text-zinc-500">{t(`${prefix}.noGeneratedResults`)}</p>
      </div>
    </section>
  );
}
