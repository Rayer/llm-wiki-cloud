'use client';

import { useState } from 'react';
import type { ProfileRequirement } from '@/lib/api';
import { useT } from '@/lib/i18n';

export type ProfileTemplateId = 'general' | 'study' | 'work' | 'life';
export type ProfileAdjustmentId = 'source' | 'comparison' | 'action';

const templateIds: ProfileTemplateId[] = ['general', 'study', 'work', 'life'];
const adjustmentIds: ProfileAdjustmentId[] = ['source', 'comparison', 'action'];

export function newProfileRequirementId(): string {
  return globalThis.crypto?.randomUUID?.() ?? `req-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

export function profileTemplateRequirements(templateId: ProfileTemplateId, t: (key: string) => string): ProfileRequirement[] {
  const prefix = `Profile.localUx.templates.${templateId}`;
  return [1, 2].map((index) => ({
    id: newProfileRequirementId(),
    text: t(`${prefix}.requirement${index}`),
  }));
}

export function ProfileTemplates({
  requirements,
  selectedTemplateId,
  disabled = false,
  onSelect,
  onApplyTemplate,
  onClearRequirements,
  onAddAdjustment,
}: {
  requirements: ProfileRequirement[];
  selectedTemplateId: ProfileTemplateId | null;
  disabled?: boolean;
  onSelect: (templateId: ProfileTemplateId | null) => void;
  onApplyTemplate: (templateId: ProfileTemplateId) => void;
  onClearRequirements: () => void;
  onAddAdjustment: (adjustmentId: ProfileAdjustmentId) => void;
}) {
  const { t } = useT();
  const [confirmation, setConfirmation] = useState<
    | { type: 'template'; templateId: ProfileTemplateId }
    | { type: 'clear' }
    | null
  >(null);
  const prefix = 'Profile.localUx';

  function requestTemplateApply() {
    if (!selectedTemplateId || disabled) return;
    if (requirements.length > 0) {
      setConfirmation({ type: 'template', templateId: selectedTemplateId });
      return;
    }
    onApplyTemplate(selectedTemplateId);
  }

  function requestClear() {
    if (disabled) return;
    if (requirements.length > 0) {
      setConfirmation({ type: 'clear' });
      return;
    }
    onClearRequirements();
  }

  function confirmChange() {
    if (!confirmation) return;
    if (confirmation.type === 'template') onApplyTemplate(confirmation.templateId);
    else onClearRequirements();
    setConfirmation(null);
  }

  return (
    <section className="mt-4 rounded-xl border border-white/10 bg-black/15 p-4" aria-labelledby="profile-template-heading">
      <h4 id="profile-template-heading" className="text-sm font-semibold text-white">{t(`${prefix}.templateHeading`)}</h4>
      <p className="mt-1 text-xs leading-5 text-zinc-400">{t(`${prefix}.templateDescription`)}</p>
      <div className="mt-3 grid gap-2 sm:grid-cols-2" role="group" aria-label={t(`${prefix}.templateHeading`)}>
        {templateIds.map((templateId) => (
          <button
            key={templateId}
            type="button"
            aria-pressed={selectedTemplateId === templateId}
            disabled={disabled}
            onClick={() => {
              setConfirmation(null);
              onSelect(templateId);
            }}
            className={`min-h-16 rounded-lg border p-3 text-left transition focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-300 disabled:opacity-50 ${selectedTemplateId === templateId ? 'border-emerald-300/70 bg-emerald-300/10' : 'border-white/10 hover:bg-white/5'}`}
          >
            <span className="block text-sm font-medium text-zinc-100">{t(`${prefix}.templates.${templateId}.name`)}</span>
            <span className="mt-1 block text-xs leading-5 text-zinc-400">{t(`${prefix}.templates.${templateId}.description`)}</span>
          </button>
        ))}
      </div>
      <div className="mt-3 flex flex-wrap gap-2">
        <button
          type="button"
          disabled={disabled || selectedTemplateId === null}
          onClick={requestTemplateApply}
          className="min-h-10 rounded-lg bg-emerald-300 px-3 text-sm font-semibold text-zinc-950 hover:bg-emerald-200 disabled:cursor-not-allowed disabled:opacity-50"
        >
          {t(`${prefix}.applyTemplate`)}
        </button>
        <button
          type="button"
          disabled={disabled}
          onClick={requestClear}
          className="min-h-10 rounded-lg border border-white/10 px-3 text-sm text-zinc-300 hover:bg-white/5 disabled:opacity-50"
        >
          {t(`${prefix}.generalSettings`)}
        </button>
      </div>
      {confirmation ? (
        <div className="mt-3 rounded-lg border border-amber-200/30 bg-amber-200/5 p-3" role="alert">
          <p className="text-sm text-amber-100">
            {confirmation.type === 'template'
              ? t(`${prefix}.replaceTemplatePrompt`, { template: t(`${prefix}.templates.${confirmation.templateId}.name`) })
              : t(`${prefix}.clearPrompt`)}
          </p>
          <div className="mt-2 flex flex-wrap gap-2">
            <button
              type="button"
              disabled={disabled}
              onClick={confirmChange}
              className="min-h-10 rounded-md bg-amber-100 px-3 text-sm font-semibold text-zinc-950 disabled:opacity-50"
            >
              {confirmation.type === 'template' ? t(`${prefix}.confirmReplace`) : t(`${prefix}.confirmClear`)}
            </button>
            <button
              type="button"
              disabled={disabled}
              onClick={() => setConfirmation(null)}
              className="min-h-10 rounded-md border border-white/10 px-3 text-sm text-zinc-200 disabled:opacity-50"
            >
              {t(`${prefix}.cancelChange`)}
            </button>
          </div>
        </div>
      ) : null}

      <div className="mt-4 border-t border-white/10 pt-3">
        <h5 className="text-xs font-semibold uppercase tracking-wide text-zinc-300">{t(`${prefix}.adjustmentHeading`)}</h5>
        <p className="mt-1 text-xs leading-5 text-zinc-500">{t(`${prefix}.adjustmentDescription`)}</p>
        <div className="mt-2 flex flex-wrap gap-2">
          {adjustmentIds.map((adjustmentId) => (
            <button
              key={adjustmentId}
              type="button"
              disabled={disabled}
              onClick={() => onAddAdjustment(adjustmentId)}
              className="min-h-10 rounded-lg border border-white/10 px-3 text-xs text-zinc-300 hover:border-emerald-300/40 hover:text-white disabled:opacity-50"
            >
              {t(`${prefix}.adjustments.${adjustmentId}.label`)}
            </button>
          ))}
        </div>
      </div>
    </section>
  );
}
