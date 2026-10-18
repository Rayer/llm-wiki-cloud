'use client';

import { useEffect, useRef, useState } from 'react';
import { useT } from '@/lib/i18n';
import { ProfileExpectedEffect } from './ProfileExpectedEffect';

type StageId = 'source' | 'concepts' | 'query';

const stages: StageId[] = ['source', 'concepts', 'query'];

export function ProfileHowItWorks({
  open,
  mode = 'settings',
  onBack,
  onSetPreferences,
  onUseGeneralSettings,
}: {
  open: boolean;
  mode?: 'settings' | 'creation';
  onBack: () => void;
  onSetPreferences: () => void;
  onUseGeneralSettings: () => void;
}) {
  const { t } = useT();
  const [stageIndex, setStageIndex] = useState(0);
  const headingRef = useRef<HTMLHeadingElement>(null);
  const screenHeadingRef = useRef<HTMLHeadingElement>(null);
  const wasOpen = useRef(false);
  const lastStageIndex = useRef(0);
  const prefix = 'Profile.localUx.guide';
  const stageId = stages[stageIndex];

  useEffect(() => {
    if (!open) {
      wasOpen.current = false;
      return;
    }
    if (!wasOpen.current) {
      wasOpen.current = true;
      lastStageIndex.current = 0;
      setStageIndex(0);
      headingRef.current?.focus();
      return;
    }
    if (lastStageIndex.current !== stageIndex) {
      lastStageIndex.current = stageIndex;
      screenHeadingRef.current?.focus();
    }
  }, [open, stageIndex]);

  function selectStage(index: number) {
    setStageIndex(index);
  }

  return (
    <section hidden={!open} aria-labelledby="profile-guide-heading">
      <header className="mt-1">
        <button
          type="button"
          onClick={onBack}
          className="min-h-11 min-w-11 rounded-lg border border-white/10 px-3 text-sm text-zinc-200 hover:bg-white/5 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-300"
        >
          <span aria-hidden="true" className="mr-2">←</span>{t(`${prefix}.${mode === 'creation' ? 'backToCreation' : 'backToSettings'}`)}
        </button>
        <div className="mt-4">
          <p className="font-mono text-[11px] uppercase tracking-[0.2em] text-emerald-300/90">{t(`${prefix}.eyebrow`)}</p>
          <h2 id="profile-guide-heading" ref={headingRef} tabIndex={-1} className="mt-2 text-xl font-semibold text-white focus:outline-none">
            {t(`${prefix}.heading`)}
          </h2>
          <p className="mt-2 max-w-3xl text-sm leading-6 text-zinc-400">{t(`${prefix}.intro`)}</p>
          <p className="mt-2 text-xs leading-5 text-zinc-500">{t(`${prefix}.fixedExampleLabel`)}</p>
        </div>
      </header>

      <nav className="mt-4 grid grid-cols-3 gap-2" aria-label={t(`${prefix}.mapLabel`)}>
        {stages.map((id, index) => (
          <button
            key={id}
            type="button"
            aria-current={stageIndex === index ? 'step' : undefined}
            aria-controls="profile-guide-window"
            onClick={() => selectStage(index)}
            className={`min-h-11 min-w-0 rounded-lg border px-2 py-2 text-left text-xs leading-5 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-300 sm:px-3 sm:text-sm ${stageIndex === index ? 'border-emerald-300/70 bg-emerald-300/10 text-emerald-100' : 'border-white/10 text-zinc-300 hover:bg-white/5'}`}
          >
            <span className="block font-semibold">{index + 1}. {t(`${prefix}.stages.${id}.name`)}</span>
          </button>
        ))}
      </nav>

      <section id="profile-guide-window" className="mt-3 rounded-xl border border-white/10 bg-zinc-950/50 p-4 sm:p-5" aria-labelledby="profile-guide-screen-title">
        <p className="text-xs font-semibold uppercase tracking-wide text-emerald-200">
          {t(`${prefix}.position`, { index: stageIndex + 1, count: stages.length })}
        </p>
        <h3 id="profile-guide-screen-title" ref={screenHeadingRef} tabIndex={-1} className="mt-1 text-base font-semibold text-white focus:outline-none">
          {t(`${prefix}.stages.${stageId}.title`)}
        </h3>
        <p className="mt-1 text-sm leading-6 text-zinc-300">{t(`${prefix}.stages.${stageId}.description`)}</p>

        {stageId === 'source' ? (
          <div className="mt-3 rounded-lg border border-white/10 border-l-4 border-l-white/30 bg-black/20 p-4">
            <p className="text-xs font-semibold uppercase tracking-wide text-zinc-400">{t(`${prefix}.rawHeading`)}</p>
            <p className="mt-2 whitespace-pre-line text-sm leading-6 text-zinc-200">{t(`${prefix}.rawText`)}</p>
            <p className="mt-3 text-xs leading-5 text-zinc-500">{t(`${prefix}.sourceBoundary`)}</p>
          </div>
        ) : null}

        {stageId === 'concepts' ? (
          <>
            <details className="mt-3 rounded-lg border border-white/10 p-3">
              <summary className="min-h-11 cursor-pointer py-2 text-sm font-medium text-zinc-200 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-300">
                {t(`${prefix}.showRawNote`)}
              </summary>
              <p className="whitespace-pre-line text-sm leading-6 text-zinc-300">{t(`${prefix}.rawText`)}</p>
            </details>
            <section className="mt-3 rounded-xl border border-white/10 bg-black/10 p-4" aria-labelledby="profile-instructions-heading">
              <h4 id="profile-instructions-heading" className="text-sm font-semibold text-white">{t(`${prefix}.requirementsHeading`)}</h4>
              <div className="mt-3 grid gap-3 sm:grid-cols-2">
                {(['family', 'parking'] as const).map((exampleId) => (
                  <article key={exampleId} className="min-w-0 rounded-lg border border-white/10 p-3">
                    <h5 className="text-xs font-semibold uppercase tracking-wide text-sky-200">{t(`${prefix}.requirements.${exampleId}.heading`)}</h5>
                    <p className="mt-2 whitespace-pre-line text-sm leading-6 text-zinc-300">{t(`${prefix}.requirements.${exampleId}.text`)}</p>
                  </article>
                ))}
              </div>
            </section>
            <ProfileExpectedEffect />
          </>
        ) : null}

        {stageId === 'query' ? (
          <>
            <div className="mt-3 rounded-lg border border-emerald-200/20 bg-emerald-200/[0.04] p-4">
              <p className="text-xs font-semibold uppercase tracking-wide text-sky-200">{t(`${prefix}.queryHeading`)}</p>
              <p className="mt-2 text-sm font-medium leading-6 text-emerald-50">{t(`${prefix}.queryText`)}</p>
              <p className="mt-3 text-xs leading-5 text-zinc-400">{t(`${prefix}.queryBoundary`)}</p>
            </div>
            <div className="mt-3 rounded-lg border border-amber-200/20 bg-amber-200/[0.04] p-4">
              <h4 className="text-sm font-semibold text-amber-100">{t(`${prefix}.updateHeading`)}</h4>
              <p className="mt-2 text-sm leading-6 text-zinc-300">{t(`${prefix}.updateBody`)}</p>
            </div>
            {mode === 'settings' ? (
              <div className="mt-4 flex flex-wrap gap-2">
                <button
                  type="button"
                  onClick={onSetPreferences}
                  className="min-h-11 rounded-lg bg-emerald-300 px-4 text-sm font-semibold text-zinc-950 hover:bg-emerald-200 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-300"
                >
                  {t(`${prefix}.setPreferences`)}
                </button>
                <button
                  type="button"
                  onClick={onUseGeneralSettings}
                  className="min-h-11 rounded-lg border border-white/10 px-4 text-sm text-zinc-200 hover:bg-white/5 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-300"
                >
                  {t(`${prefix}.useGeneralSettings`)}
                </button>
              </div>
            ) : null}
          </>
        ) : null}

        <div className="mt-4 flex justify-between gap-2 border-t border-white/10 pt-3">
          <button
            type="button"
            disabled={stageIndex === 0}
            onClick={() => selectStage(stageIndex - 1)}
            className="min-h-11 rounded-lg border border-white/10 px-3 text-sm text-zinc-200 hover:bg-white/5 disabled:cursor-not-allowed disabled:opacity-40 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-300"
          >
            {t(`${prefix}.previous`)}
          </button>
          {stageIndex < stages.length - 1 ? (
            <button
              type="button"
              onClick={() => selectStage(stageIndex + 1)}
              className="min-h-11 rounded-lg border border-emerald-200/20 px-3 text-sm text-emerald-100 hover:bg-emerald-200/5 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-300"
            >
              {t(`${prefix}.next`, { stage: t(`${prefix}.stages.${stages[stageIndex + 1]}.name`) })}
            </button>
          ) : null}
        </div>
      </section>
    </section>
  );
}
