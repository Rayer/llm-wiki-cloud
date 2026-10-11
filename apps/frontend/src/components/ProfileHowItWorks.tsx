'use client';

import { useEffect, useRef, useState } from 'react';
import { useT } from '@/lib/i18n';

type StageId = 'raw' | 'pipeline' | 'knowledge' | 'query';

const stages: StageId[] = ['raw', 'pipeline', 'knowledge', 'query'];

export function ProfileHowItWorks() {
  const { t } = useT();
  const [open, setOpen] = useState(false);
  const [stageIndex, setStageIndex] = useState(0);
  const [profileExampleOpen, setProfileExampleOpen] = useState(false);
  const [mode, setMode] = useState<'general' | 'personal'>('general');
  const [parking, setParking] = useState(false);
  const [children, setChildren] = useState(false);
  const headingRef = useRef<HTMLHeadingElement>(null);
  const focusHeadingAfterChange = useRef(false);
  const prefix = 'Profile.localUx.howItWorks';
  const stageId = stages[stageIndex];

  useEffect(() => {
    if (!focusHeadingAfterChange.current) return;
    headingRef.current?.focus();
    focusHeadingAfterChange.current = false;
  }, [stageIndex]);

  function selectStage(index: number) {
    focusHeadingAfterChange.current = true;
    setStageIndex(index);
  }

  const selectedPreferences = mode === 'personal'
    ? [
      parking ? t(`${prefix}.parkingPreference`) : '',
      children ? t(`${prefix}.childrenPreference`) : '',
    ].filter(Boolean)
    : [];
  const conceptExampleKey = selectedPreferences.length === 0
    ? 'conceptExampleGeneral'
    : parking && children
      ? 'conceptExampleParkingAndChildren'
      : parking
        ? 'conceptExampleParking'
        : 'conceptExampleChildren';

  return (
    <section className="mt-5 rounded-xl border border-white/10 bg-black/15 p-4">
      <button
        type="button"
        aria-expanded={open}
        aria-controls="profile-how-it-works-panel"
        onClick={() => setOpen((value) => !value)}
        className="min-h-11 rounded-lg border border-white/10 px-3 text-sm font-medium text-zinc-200 hover:bg-white/5"
      >
        {open ? t(`${prefix}.closeEntry`) : t(`${prefix}.openEntry`)}
      </button>
      <div id="profile-how-it-works-panel" hidden={!open}>
        <div className="mt-4">
          <h3 className="text-base font-semibold text-white">{t(`${prefix}.heading`)}</h3>
          <p className="mt-1 text-sm leading-6 text-zinc-400">{t(`${prefix}.intro`)}</p>
          <p className="mt-2 text-xs text-zinc-500">{t(`${prefix}.fixedExampleLabel`)}</p>
        </div>

        <div className="mt-3 grid grid-cols-2 gap-2 lg:grid-cols-4" role="group" aria-label={t(`${prefix}.mapLabel`)}>
          {stages.map((id, index) => (
            <button
              key={id}
              type="button"
              aria-pressed={stageId === id}
              aria-controls="profile-how-stage-window"
              onClick={() => selectStage(index)}
              className={`min-h-14 rounded-lg border px-3 py-2 text-left focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-300 ${stageId === id ? 'border-emerald-300/70 bg-emerald-300/10' : 'border-white/10 hover:bg-white/5'}`}
            >
              <span className="block text-xs font-semibold text-zinc-100">{index + 1}. {t(`${prefix}.stages.${id}.name`)}</span>
              <span className="mt-0.5 block text-[11px] text-zinc-500">{t(`${prefix}.stages.${id}.term`)}</span>
            </button>
          ))}
        </div>

        <section id="profile-how-stage-window" className="mt-3 rounded-xl border border-white/10 bg-zinc-950/50 p-4 sm:p-5" aria-labelledby="profile-how-stage-title">
          <p className="text-xs font-semibold uppercase tracking-wide text-emerald-200">
            {t(`${prefix}.position`, { index: stageIndex + 1, count: stages.length, term: t(`${prefix}.stages.${stageId}.term`) })}
          </p>
          <h4 id="profile-how-stage-title" ref={headingRef} tabIndex={-1} className="mt-1 text-base font-semibold text-white focus:outline-none">
            {t(`${prefix}.stages.${stageId}.title`)}
          </h4>
          <p className="mt-1 text-sm leading-6 text-zinc-300">{t(`${prefix}.stages.${stageId}.description`)}</p>

          {stageId === 'pipeline' ? (
            <div className="mt-3 rounded-lg border border-white/10 bg-white/[0.03] p-3">
              <p className="text-xs font-semibold uppercase tracking-wide text-sky-200">{t(`${prefix}.pipelineFocusHeading`)}</p>
              <p className="mt-1 text-sm leading-6 text-zinc-200">
                {selectedPreferences.length > 0
                  ? t(`${prefix}.pipelineFocusWithPreferences`, { preferences: selectedPreferences.join(t(`${prefix}.preferenceSeparator`)) })
                  : t(`${prefix}.pipelineFocusGeneral`)}
              </p>
              <p className="mt-2 text-xs leading-5 text-zinc-400">{t(`${prefix}.stages.${stageId}.boundary`)}</p>
            </div>
          ) : stageId === 'knowledge' ? (
            <div className="mt-3 grid gap-3 sm:grid-cols-2">
              <section className="rounded-lg border border-white/10 p-3" aria-labelledby="profile-how-concept-heading">
                <h5 id="profile-how-concept-heading" className="text-xs font-semibold uppercase tracking-wide text-sky-200">{t(`${prefix}.conceptHeading`)}</h5>
                <p className="mt-2 whitespace-pre-line text-sm leading-6 text-zinc-200">{t(`${prefix}.${conceptExampleKey}`)}</p>
              </section>
              <section className="rounded-lg border border-white/10 p-3" aria-labelledby="profile-how-tags-heading">
                <h5 id="profile-how-tags-heading" className="text-xs font-semibold uppercase tracking-wide text-amber-100">{t(`${prefix}.tagsHeading`)}</h5>
                <p className="mt-2 whitespace-pre-line text-sm leading-6 text-zinc-200">
                  {selectedPreferences.length > 0
                    ? t(`${prefix}.tagExampleWithPreferences`, { preferences: selectedPreferences.join(t(`${prefix}.preferenceSeparator`)) })
                    : t(`${prefix}.tagExampleGeneral`)}
                </p>
                <p className="mt-2 text-xs leading-5 text-zinc-400">{t(`${prefix}.unknownRemainsUnknown`)}</p>
              </section>
            </div>
          ) : stageId === 'query' ? (
            <div className="mt-3 rounded-lg border border-white/10 bg-white/[0.03] p-3">
              <p className="text-xs font-semibold uppercase tracking-wide text-sky-200">{t(`${prefix}.requiredScopeHeading`)}</p>
              <p className="mt-1 text-sm font-medium text-zinc-100">{t(`${prefix}.requiredScopeQuery`)}</p>
              <p className="mt-3 text-xs font-semibold uppercase tracking-wide text-amber-100">{t(`${prefix}.preferenceComparisonHeading`)}</p>
              <p className="mt-1 text-sm leading-6 text-zinc-300">
                {selectedPreferences.length > 0
                  ? t(`${prefix}.preferenceComparison`, { preferences: selectedPreferences.join(t(`${prefix}.preferenceSeparator`)) })
                  : t(`${prefix}.noPreferenceComparison`)}
              </p>
              <p className="mt-3 text-xs leading-5 text-zinc-400">{t(`${prefix}.queryLimits`)}</p>
            </div>
          ) : (
            <div className="mt-3 rounded-lg border border-white/10 bg-white/[0.03] p-3">
              <p className="text-xs font-semibold uppercase tracking-wide text-sky-200">{t(`${prefix}.sourceExampleHeading`)}</p>
              <p className="mt-1 whitespace-pre-line text-sm leading-6 text-zinc-200">{t(`${prefix}.sourceExample`)}</p>
              <p className="mt-2 text-xs leading-5 text-zinc-400">{t(`${prefix}.stages.${stageId}.boundary`)}</p>
            </div>
          )}

          <button
            type="button"
            aria-expanded={profileExampleOpen}
            aria-controls="profile-how-profile-example"
            onClick={() => setProfileExampleOpen((value) => !value)}
            className="mt-4 min-h-10 rounded-lg border border-violet-200/20 px-3 text-sm text-violet-100 hover:bg-violet-200/5"
          >
            {profileExampleOpen ? t(`${prefix}.hideProfileExample`) : t(`${prefix}.showProfileExample`)}
          </button>
          <section id="profile-how-profile-example" hidden={!profileExampleOpen} className="mt-3 rounded-lg border border-violet-200/15 bg-violet-200/5 p-3" aria-labelledby="profile-how-profile-heading">
            <h5 id="profile-how-profile-heading" className="text-sm font-semibold text-violet-100">{t(`${prefix}.profileExampleHeading`)}</h5>
            <p className="mt-1 text-xs leading-5 text-zinc-400">{t(`${prefix}.profileExampleDescription`)}</p>
            <fieldset className="mt-3">
              <legend className="text-xs font-medium text-zinc-300">{t(`${prefix}.modeLegend`)}</legend>
              <div className="mt-2 flex flex-wrap gap-x-5 gap-y-2">
                <label className="flex min-h-10 items-center gap-2 text-sm text-zinc-200">
                  <input
                    type="radio"
                    name="profile-how-example-mode"
                    value="general"
                    checked={mode === 'general'}
                    onChange={() => setMode('general')}
                  />
                  {t(`${prefix}.generalMode`)}
                </label>
                <label className="flex min-h-10 items-center gap-2 text-sm text-zinc-200">
                  <input
                    type="radio"
                    name="profile-how-example-mode"
                    value="personal"
                    checked={mode === 'personal'}
                    onChange={() => setMode('personal')}
                  />
                  {t(`${prefix}.personalMode`)}
                </label>
              </div>
            </fieldset>
            {mode === 'personal' ? (
              <fieldset className="mt-3">
                <legend className="text-xs font-medium text-zinc-300">{t(`${prefix}.optionalPreferencesLegend`)}</legend>
                <div className="mt-2 flex flex-wrap gap-x-5 gap-y-2">
                  <label className="flex min-h-10 items-center gap-2 text-sm text-zinc-200">
                    <input type="checkbox" checked={parking} onChange={(event) => setParking(event.target.checked)} />
                    {t(`${prefix}.parkingPreference`)}
                  </label>
                  <label className="flex min-h-10 items-center gap-2 text-sm text-zinc-200">
                    <input type="checkbox" checked={children} onChange={(event) => setChildren(event.target.checked)} />
                    {t(`${prefix}.childrenPreference`)}
                  </label>
                </div>
              </fieldset>
            ) : null}
            <p className="mt-2 text-xs leading-5 text-violet-100/80">{t(`${prefix}.storyOnlyNotice`)}</p>
          </section>

          <div className="mt-4 flex flex-wrap justify-between gap-2 border-t border-white/10 pt-3">
            <button
              type="button"
              disabled={stageIndex === 0}
              onClick={() => selectStage(stageIndex - 1)}
              className="min-h-10 rounded-lg border border-white/10 px-3 text-sm text-zinc-200 hover:bg-white/5 disabled:cursor-not-allowed disabled:opacity-40"
            >
              {t(`${prefix}.previous`)}
            </button>
            {stageIndex < stages.length - 1 ? (
              <button
                type="button"
                onClick={() => selectStage(stageIndex + 1)}
                className="min-h-10 rounded-lg border border-emerald-200/20 px-3 text-sm text-emerald-100 hover:bg-emerald-200/5"
              >
                {t(`${prefix}.next`, { stage: t(`${prefix}.stages.${stages[stageIndex + 1]}.name`) })}
              </button>
            ) : null}
          </div>
        </section>
      </div>
    </section>
  );
}
