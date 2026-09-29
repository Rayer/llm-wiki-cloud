'use client';

import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import {
  confirmProfileCandidate,
  confirmProfileBootstrapGuidance,
  getProfile,
  getProfileBootstrapGuidance,
  getProfileGuidanceArtifact,
  getProfileJob,
  retryProfileCandidate,
  retryProfileDerivation,
  saveProfile,
  type ProfileRequirement,
  type ProfileBootstrapGuidance,
  type ProfileGuidanceArtifact,
  type ProfileState,
} from '@/lib/api';
import { useT } from '@/lib/i18n';
import { ProfileRequirementsEditor } from './ProfileRequirementsEditor';

function sameRequirements(left: ProfileRequirement[], right: ProfileRequirement[]): boolean {
  return left.length === right.length
    && left.every((item, index) => item.id === right[index]?.id && item.text === right[index]?.text);
}

function errorStatus(error: unknown): number | undefined {
  return error && typeof error === 'object' && 'status' in error
    && typeof error.status === 'number'
    ? error.status
    : undefined;
}

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error ? error.message : fallback;
}

function scheduleLabel(value: string): string {
  return value;
}

function requirementDispositionLabel(value: ProfileBootstrapGuidance['preview']['requirements'][number]['disposition'], t: (key: string) => string): string {
  switch (value) {
    case 'compile_guidance': return t('Profile.disposition.compileGuidance');
    case 'dictionary_or_query': return t('Profile.disposition.dictionaryOrQuery');
    case 'both': return t('Profile.disposition.both');
    case 'limitation': return t('Profile.disposition.limitation');
  }
}

function ProfileGuidanceArtifactDetails({ projectId, revision, title }: { projectId: string; revision: string; title: string }) {
  const { t } = useT();
  const [result, setResult] = useState<{
    projectId: string;
    revision: string;
    artifact?: ProfileGuidanceArtifact;
    error?: string;
  } | null>(null);

  useEffect(() => {
    let active = true;
    getProfileGuidanceArtifact(projectId, revision)
      .then(({ guidance_artifact: artifact }) => {
        if (!active) return;
        if (artifact.revision !== revision) {
          setResult({ projectId, revision, error: t('Profile.guidanceRevisionMismatch') });
          return;
        }
        setResult({ projectId, revision, artifact });
      })
      .catch((readError: unknown) => {
        if (active) setResult({ projectId, revision, error: errorMessage(readError, t('Profile.requestFailed')) });
      });
    return () => { active = false; };
  }, [projectId, revision, t]);

  const current = result?.projectId === projectId && result.revision === revision ? result : null;
  if (current?.error) return <p className="mt-3 text-sm text-amber-200" role="alert">{t('Profile.guidanceReadError', { error: current.error })}</p>;
  if (!current?.artifact) return <p className="mt-3 text-sm text-zinc-400" role="status">{t('Profile.loadingImmutableGuidance')}</p>;

  return (
    <section className="mt-4 rounded-lg border border-white/10 p-3" aria-label={title}>
      <h4 className="text-xs font-semibold uppercase tracking-wide text-sky-200">{title}</h4>
      <p className="mt-2 break-all text-xs leading-5 text-zinc-400">
        {t('Profile.revisionLabel')} <code>{current.artifact.revision}</code>
      </p>
      <p className="text-xs leading-5 text-zinc-400">
        {t('Profile.artifactVersions', { model: current.artifact.model_version, prompt: current.artifact.prompt_version, schema: current.artifact.schema_version })}
      </p>
      <p className="mt-2 whitespace-pre-wrap text-sm leading-6 text-zinc-200">
        {current.artifact.compile_guidance || t('Profile.noCompileGuidance')}
      </p>
      <p className="mt-2 text-xs leading-5 text-zinc-500">
        {t('Profile.adapterBudgetNote')}
      </p>
    </section>
  );
}

type RequestScope = { projectId: string; identity: object };

export function ProjectProfilePanel({ projectId }: { projectId: string }) {
  const { t } = useT();
  const [profile, setProfile] = useState<ProfileState | null>(null);
  const [bootstrapGuidance, setBootstrapGuidance] = useState<ProfileBootstrapGuidance | null>(null);
  const [bootstrapScope, setBootstrapScope] = useState<RequestScope | null>(null);
  const [bootstrapLoading, setBootstrapLoading] = useState(false);
  const [bootstrapError, setBootstrapError] = useState('');
  const [draft, setDraft] = useState<ProfileRequirement[]>([]);
  const [loading, setLoading] = useState(true);
  const [savingScope, setSavingScope] = useState<RequestScope | null>(null);
  const [workingScope, setWorkingScope] = useState<RequestScope | null>(null);
  const [error, setError] = useState('');
  const [errorProjectId, setErrorProjectId] = useState('');
  const [notice, setNotice] = useState('');
  const profileRef = useRef<ProfileState | null>(null);
  const bootstrapRequestRef = useRef(0);
  const loadedRef = useRef(false);
  const currentScope = useMemo<RequestScope>(() => ({ projectId, identity: {} }), [projectId]);
  const currentScopeRef = useRef<RequestScope | null>(null);
  const mountedRef = useRef(false);

  useLayoutEffect(() => {
    currentScopeRef.current = currentScope;
    return () => {
      if (currentScopeRef.current === currentScope) currentScopeRef.current = null;
    };
  }, [currentScope]);

  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; };
  }, []);

  const requestIsCurrent = useCallback((scope: RequestScope) => (
    mountedRef.current
    && currentScopeRef.current === scope
  ), []);
  const loadBootstrapGuidance = useCallback(async (scope: RequestScope, expectedProfileRevision: number) => {
    const requestId = ++bootstrapRequestRef.current;
    setBootstrapScope(scope);
    setBootstrapLoading(true);
    setBootstrapError('');
    try {
      const result = await getProfileBootstrapGuidance(scope.projectId);
      if (!requestIsCurrent(scope) || requestId !== bootstrapRequestRef.current) return null;
      const current = profileRef.current;
      if (current?.project_id !== scope.projectId || current.revision !== expectedProfileRevision) return null;
      const next = result.bootstrap_guidance;
      const currentGuidance = next?.profile_revision === expectedProfileRevision ? next : null;
      setBootstrapGuidance(currentGuidance);
      return currentGuidance;
    } catch (readError: unknown) {
      if (requestIsCurrent(scope) && requestId === bootstrapRequestRef.current) {
        setBootstrapError(errorMessage(readError, t('Profile.requestFailed')));
      }
      return null;
    } finally {
      if (requestIsCurrent(scope) && requestId === bootstrapRequestRef.current) setBootstrapLoading(false);
    }
  }, [requestIsCurrent, t]);
  const saving = savingScope === currentScope;
  const working = workingScope === currentScope;

  const showError = useCallback((message: string, targetProjectId = projectId) => {
    setError(message);
    setErrorProjectId(targetProjectId);
  }, [projectId]);

  const applyProfile = useCallback((next: ProfileState) => {
    const previous = profileRef.current;
    if (
      loadedRef.current
      && previous?.project_id === next.project_id
      && previous.active?.candidate_id !== next.active?.candidate_id
      && next.active
    ) {
      setNotice(t('Profile.activeNotice', { candidateId: next.active.candidate_id }));
    }
    profileRef.current = next;
    setProfile(next);
  }, [t]);

  useEffect(() => {
    let active = true;
    loadedRef.current = false;
    profileRef.current = null;
    bootstrapRequestRef.current += 1;

    getProfile(projectId)
      .then((next) => {
        if (!active) return;
        if (next.project_id !== projectId) {
          showError(t('Profile.responseProjectMismatch'));
          return;
        }
        profileRef.current = next;
        loadedRef.current = true;
        setProfile(next);
        setDraft(next.requirements.map((item) => ({ ...item })));
        setError('');
        setErrorProjectId('');
        setNotice('');
        if (next.requirements.length > 0 && next.active === null) {
          void loadBootstrapGuidance(currentScope, next.revision);
        }
      })
      .catch((loadError: unknown) => {
        if (active) showError(t('Profile.errors.load', { error: errorMessage(loadError, t('Profile.requestFailed')) }));
      })
      .finally(() => {
        if (active) setLoading(false);
      });

    return () => { active = false; };
  }, [currentScope, loadBootstrapGuidance, projectId, showError, t]);

  useEffect(() => {
    const current = profile?.project_id === projectId ? profile : null;
    if (!current) return;
    const derivationPending = current.derivation_status === 'pending' && current.candidate === null;
    const jobPending = current.job !== null
      && ['scheduled', 'running', 'retry_wait'].includes(current.job.status);
    if (!derivationPending && !jobPending) return;

    let active = true;
    const timer = window.setInterval(() => {
      if (derivationPending) {
        getProfile(projectId)
          .then((next) => {
            if (active && next.project_id === projectId) applyProfile(next);
          })
          .catch(() => undefined);
        if (current.requirements.length > 0 && current.active === null) {
          void loadBootstrapGuidance(currentScope, current.revision);
        }
      } else if (current.job) {
        const jobId = current.job.job_id;
        getProfileJob(projectId, jobId)
          .then(async (job) => {
            if (!active) return;
            if (job.status === 'ready') {
              try {
                const next = await getProfile(projectId);
                if (active && next.project_id === projectId) applyProfile(next);
              } catch {
                // Keep polling the last known pending job until Profile confirms the transition.
              }
              return;
            }
            setProfile((latest) => (
              latest?.project_id === projectId && latest.job?.job_id === jobId
                ? { ...latest, job }
                : latest
            ));
          })
          .catch(() => undefined);
      }
    }, 5000);

    return () => {
      active = false;
      window.clearInterval(timer);
    };
  }, [applyProfile, currentScope, loadBootstrapGuidance, profile, projectId]);

  const visibleProfile = profile?.project_id === projectId ? profile : null;
  const bootstrapForCurrentScope = bootstrapScope === currentScope;
  const visibleBootstrapGuidance = bootstrapForCurrentScope ? bootstrapGuidance : null;
  const visibleBootstrapLoading = bootstrapForCurrentScope && bootstrapLoading;
  const visibleBootstrapError = bootstrapForCurrentScope ? bootstrapError : '';
  const visibleError = errorProjectId === projectId ? error : '';
  const profileLoading = loading || (!visibleProfile && !visibleError);
  const visibleNotice = visibleProfile ? notice : '';
  const showFirstCompileBootstrap = Boolean(visibleProfile && visibleProfile.requirements.length > 0 && visibleProfile.active === null);
  const bootstrapMatchesProfile = Boolean(
    visibleBootstrapGuidance
    && visibleProfile
    && visibleBootstrapGuidance.profile_revision === visibleProfile.revision,
  );
  const bootstrapMatchesDraft = Boolean(visibleProfile && sameRequirements(draft, visibleProfile.requirements));

  async function saveRequirements() {
    if (!visibleProfile || saving || working) return;
    const scope = currentScope;
    setSavingScope(scope);
    setError('');
    setErrorProjectId(projectId);
    setNotice('');
    try {
      const next = await saveProfile(projectId, visibleProfile.revision, draft);
      if (!requestIsCurrent(scope) || next.project_id !== projectId) return;
      applyProfile(next);
      setDraft(next.requirements.map((item) => ({ ...item })));
      setBootstrapGuidance(null);
      setBootstrapError('');
      if (next.requirements.length > 0 && next.active === null) {
        void loadBootstrapGuidance(scope, next.revision);
      } else {
        setBootstrapLoading(false);
      }
      setError('');
    } catch (saveError: unknown) {
      if (!requestIsCurrent(scope)) return;
      const status = errorStatus(saveError);
      try {
        const latest = await getProfile(projectId);
        if (!requestIsCurrent(scope)) return;
        if (latest.project_id === projectId) {
          applyProfile(latest);
          setBootstrapGuidance(null);
          if (latest.requirements.length > 0 && latest.active === null) {
            void loadBootstrapGuidance(scope, latest.revision);
          } else {
            setBootstrapLoading(false);
          }
          if (sameRequirements(latest.requirements, draft)) {
            setDraft(latest.requirements.map((item) => ({ ...item })));
            setError('');
            setNotice(t('Profile.requirementsAlreadySaved'));
            return;
          }
        }
      } catch {
        // The user draft stays in the editor when the latest server state is unavailable.
      }
      if (!requestIsCurrent(scope)) return;
      showError(status === 409
        ? t('Profile.errors.profileChanged')
        : t('Profile.errors.saveUnconfirmed', { error: errorMessage(saveError, t('Profile.requestFailed')) }));
    } finally {
      if (requestIsCurrent(scope)) setSavingScope(null);
    }
  }

  async function retryDerivation() {
    if (!visibleProfile || working) return;
    const scope = currentScope;
    setWorkingScope(scope);
    setError('');
    setNotice('');
    try {
      const next = await retryProfileDerivation(projectId, visibleProfile.revision);
      if (!requestIsCurrent(scope) || next.project_id !== projectId) return;
      applyProfile(next);
      if (next.requirements.length > 0 && next.active === null) {
        void loadBootstrapGuidance(scope, next.revision);
      }
    } catch (retryError: unknown) {
      if (!requestIsCurrent(scope)) return;
      showError(t('Profile.errors.previewRetry', { error: errorMessage(retryError, t('Profile.requestFailed')) }));
    } finally {
      if (requestIsCurrent(scope)) setWorkingScope(null);
    }
  }

  async function confirmBootstrapGuidance() {
    const guidance = visibleBootstrapGuidance;
    if (
      !visibleProfile
      || !guidance
      || guidance.status !== 'preview_ready'
      || guidance.profile_revision !== visibleProfile.revision
      || !sameRequirements(draft, visibleProfile.requirements)
      || saving
      || working
    ) return;

    const scope = currentScope;
    setWorkingScope(scope);
    setError('');
    setNotice('');
    try {
      const result = await confirmProfileBootstrapGuidance(
        projectId,
        guidance.revision,
        visibleProfile.revision,
        guidance.input_digest,
      );
      if (!requestIsCurrent(scope)) return;
      const confirmed = result.bootstrap_guidance;
      if (
        !confirmed
        || confirmed.revision !== guidance.revision
        || confirmed.input_digest !== guidance.input_digest
        || confirmed.profile_revision !== visibleProfile.revision
        || confirmed.status !== 'confirmed'
        || !confirmed.confirmed_at
      ) {
        throw new Error(t('Profile.errors.bootstrapPreviewNotConfirmed'));
      }
      bootstrapRequestRef.current += 1;
      setBootstrapLoading(false);
      setBootstrapGuidance(confirmed);
      setBootstrapScope(scope);
      setNotice(t('Profile.bootstrapConfirmedNotice'));
    } catch (confirmError: unknown) {
      if (!requestIsCurrent(scope)) return;
      if (errorStatus(confirmError) === 409) {
        let profileReadbackSucceeded = false;
        try {
          const latest = await getProfile(projectId);
          if (!requestIsCurrent(scope)) return;
          if (latest.project_id === projectId) {
            profileReadbackSucceeded = true;
            applyProfile(latest);
            setBootstrapGuidance(null);
            if (latest.requirements.length > 0 && latest.active === null) {
              await loadBootstrapGuidance(scope, latest.revision);
            } else {
              setBootstrapLoading(false);
            }
          }
        } catch {
          // The user draft remains available if the latest server state cannot be read.
        }
        if (!requestIsCurrent(scope)) return;
        showError(profileReadbackSucceeded
          ? t('Profile.errors.bootstrapChangedReloaded')
          : t('Profile.errors.bootstrapChangedUnconfirmed'));
      } else {
        showError(t('Profile.errors.guidanceConfirm', { error: errorMessage(confirmError, t('Profile.requestFailed')) }));
      }
    } finally {
      if (requestIsCurrent(scope)) setWorkingScope(null);
    }
  }

  async function confirmCandidate() {
    const candidate = visibleProfile?.candidate;
    if (!visibleProfile || !candidate || candidate.source !== 'manual' || working) return;
    const scope = currentScope;
    setWorkingScope(scope);
    setError('');
    setNotice('');
    try {
      const previousActiveId = profileRef.current?.active?.candidate_id;
      const next = await confirmProfileCandidate(projectId, candidate.candidate_id, visibleProfile.revision);
      if (!requestIsCurrent(scope) || next.project_id !== projectId) return;
      applyProfile(next);
      if (next.active?.candidate_id !== candidate.candidate_id || previousActiveId === candidate.candidate_id) {
        setNotice(t('Profile.previewConfirmedNotice'));
      }
    } catch (confirmError: unknown) {
      if (!requestIsCurrent(scope)) return;
      showError(t('Profile.errors.confirmation', { error: errorMessage(confirmError, t('Profile.requestFailed')) }));
    } finally {
      if (requestIsCurrent(scope)) setWorkingScope(null);
    }
  }

  async function retryTagging() {
    const candidate = visibleProfile?.candidate;
    if (!visibleProfile || !candidate || working) return;
    const scope = currentScope;
    setWorkingScope(scope);
    setError('');
    setNotice('');
    try {
      const next = await retryProfileCandidate(projectId, candidate.candidate_id, visibleProfile.revision);
      if (!requestIsCurrent(scope) || next.project_id !== projectId) return;
      applyProfile(next);
    } catch (retryError: unknown) {
      if (!requestIsCurrent(scope)) return;
      showError(t('Profile.errors.taggingRetry', { error: errorMessage(retryError, t('Profile.requestFailed')) }));
    } finally {
      if (requestIsCurrent(scope)) setWorkingScope(null);
    }
  }

  const isSavingDisabled = !visibleProfile || saving || working
    || sameRequirements(draft, visibleProfile?.requirements ?? []);
  const canRetryTagging = visibleProfile?.job
    && (visibleProfile.job.status === 'incomplete' || visibleProfile.job.status === 'retry_wait');

  return (
    <section
      aria-labelledby="project-profile-heading"
      className="rounded-[var(--radius-lg)] border border-white/10 bg-zinc-900/45 p-5 backdrop-blur-sm sm:p-6"
    >
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <p className="font-mono text-[11px] uppercase tracking-[0.2em] text-emerald-300/90">{t('Profile.settings')}</p>
          <h2 id="project-profile-heading" className="mt-2 text-xl font-semibold text-white">{t('Profile.title')}</h2>
          <p className="mt-2 max-w-3xl text-sm leading-6 text-zinc-400">
            {t('Profile.intro')}
          </p>
        </div>
      </div>

      {profileLoading ? <p className="mt-5 text-sm text-zinc-400">{t('Profile.loading')}</p> : null}
      {!profileLoading && !visibleProfile ? (
        <div className="mt-5" role="alert">
          <p className="text-sm text-red-200">{visibleError || t('Profile.unavailable')}</p>
          <button
            type="button"
            onClick={() => {
              const scope = currentScope;
              setLoading(true);
              getProfile(projectId)
                .then((next) => {
                  if (!requestIsCurrent(scope)) return;
                  if (next.project_id !== projectId) {
                    showError(t('Profile.responseProjectMismatch'));
                    return;
                  }
                  profileRef.current = next;
                  loadedRef.current = true;
                  setProfile(next);
                  setDraft(next.requirements.map((item) => ({ ...item })));
                  setError('');
                })
                .catch((loadError: unknown) => {
                  if (requestIsCurrent(scope)) {
                    showError(t('Profile.errors.load', { error: errorMessage(loadError, t('Profile.requestFailed')) }));
                  }
                })
                .finally(() => {
                  if (requestIsCurrent(scope)) setLoading(false);
                });
            }}
            className="mt-2 min-h-10 rounded-md border border-white/10 px-3 text-sm text-zinc-200 hover:bg-white/5"
          >
            {t('Profile.retryLoading')}
          </button>
        </div>
      ) : null}

      {visibleProfile ? (
        <>
          {visibleProfile.active ? (
            <p className="mt-5 text-sm text-emerald-200" role="status">
              {t('Profile.active', { candidateId: visibleProfile.active.candidate_id })}
            </p>
          ) : (
            <p className="mt-5 text-sm text-zinc-400" role="status">{t('Profile.noActive')}</p>
          )}

          {showFirstCompileBootstrap ? (
            <section className="mt-5 rounded-xl border border-sky-300/20 bg-sky-300/5 p-4" aria-labelledby="profile-bootstrap-heading" aria-busy={visibleBootstrapLoading}>
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div>
                  <h3 id="profile-bootstrap-heading" className="text-sm font-semibold text-white">{t('Profile.firstCompileGuidance')}</h3>
                  <p className="mt-1 text-xs leading-5 text-zinc-400">
                    {t('Profile.firstCompileDescription')}
                  </p>
                </div>
                {visibleBootstrapGuidance?.status === 'preview_ready' ? (
                  <button
                    type="button"
                    disabled={saving || working || !bootstrapMatchesProfile || !bootstrapMatchesDraft}
                    onClick={() => void confirmBootstrapGuidance()}
                    className="min-h-10 rounded-lg bg-sky-200 px-3 text-sm font-semibold text-zinc-950 hover:bg-sky-100 disabled:cursor-not-allowed disabled:opacity-50"
                  >
                    {working ? t('Profile.confirming') : t('Profile.confirmForFirstCompile')}
                  </button>
                ) : null}
              </div>

              {visibleBootstrapGuidance ? (
                <>
                  {!bootstrapMatchesProfile ? (
                    <p className="mt-3 text-sm text-amber-100" role="status">{t('Profile.bootstrapOutOfDate')}</p>
                  ) : !bootstrapMatchesDraft ? (
                    <p className="mt-3 text-sm text-amber-100" role="status">{t('Profile.saveBeforeBootstrap')}</p>
                  ) : visibleBootstrapGuidance.status === 'confirmed' ? (
                    <p className="mt-3 text-sm text-emerald-200" role="status">
                      {visibleBootstrapGuidance.confirmed_at
                        ? t('Profile.bootstrapConfirmedAt', { time: visibleBootstrapGuidance.confirmed_at })
                        : t('Profile.bootstrapConfirmedWithoutTime')}
                    </p>
                  ) : (
                    <p className="mt-3 text-sm text-amber-100" role="status">{t('Profile.bootstrapPreviewReady')}</p>
                  )}

                  <div className="mt-4 rounded-lg border border-white/10 p-3">
                    <h4 className="text-xs font-semibold uppercase tracking-wide text-sky-200">{t('Profile.guidancePreview')}</h4>
                    <p className="mt-2 whitespace-pre-wrap text-sm leading-6 text-zinc-300">
                      {visibleBootstrapGuidance.preview.guidance_diff || t('Profile.noCompileGuidanceChanges')}
                    </p>
                  </div>
                  <ProfileGuidanceArtifactDetails
                    projectId={projectId}
                    revision={visibleBootstrapGuidance.revision}
                    title={t('Profile.bootstrapWritingGuidance')}
                  />

                  <div className="mt-4" aria-label={t('Profile.requirementEffects')}>
                    <h4 className="text-xs font-semibold uppercase tracking-wide text-zinc-300">{t('Profile.requirementEffects')}</h4>
                    <ul className="mt-2 space-y-2">
                      {visibleBootstrapGuidance.preview.requirements.map((requirement) => (
                        <li key={requirement.id} className="rounded-lg border border-white/10 p-3">
                          <p className="text-sm font-medium text-zinc-200">
                            {requirement.id}: {requirementDispositionLabel(requirement.disposition, t)}
                          </p>
                          <p className="mt-1 text-sm leading-5 text-zinc-400">{requirement.explanation}</p>
                        </li>
                      ))}
                    </ul>
                  </div>
                </>
              ) : (
                <p className="mt-3 text-sm leading-6 text-zinc-300" role="status">
                  {visibleBootstrapLoading
                    ? t('Profile.bootstrapChecking')
                    : visibleBootstrapError
                      ? t('Profile.bootstrapStatusUnavailable')
                      : visibleProfile.derivation_status === 'pending'
                        ? t('Profile.bootstrapPending')
                        : visibleProfile.derivation_status === 'failed'
                          ? t('Profile.bootstrapNotProduced')
                          : t('Profile.waitingForBootstrap')}
                </p>
              )}
              {visibleBootstrapError ? <p className="mt-2 text-xs text-amber-200" role="alert">{t('Profile.bootstrapReadError', { error: visibleBootstrapError })}</p> : null}
            </section>
          ) : null}

          {visibleProfile.active ? (
            <ProfileGuidanceArtifactDetails
              projectId={projectId}
              revision={visibleProfile.active.guidance_revision}
              title={t('Profile.activeWritingGuidance')}
            />
          ) : null}

          {visibleProfile.derivation_status === 'pending' ? (
            <p className="mt-3 text-sm leading-6 text-amber-100" role="status">
              {t('Profile.generationScheduledPrefix')}{' '}
              <time dateTime={visibleProfile.scheduled_for ?? undefined}>{scheduleLabel(visibleProfile.scheduled_for ?? t('Profile.later'))}</time>
              {t('Profile.generationScheduledSuffix')}
            </p>
          ) : null}
          {visibleProfile.derivation_status === 'failed' ? (
            <div className="mt-3 flex flex-wrap items-center gap-3" role="status">
              <p className="text-sm text-red-200">
                {visibleProfile.derivation_error_code === 'runtime_retry_exhausted'
                  ? t('Profile.runtimeRetriesExhausted')
                  : t('Profile.previewGenerationFailed')}
                {visibleProfile.derivation_error_code ? ` (${visibleProfile.derivation_error_code})` : ''}.
              </p>
              <button
                type="button"
                disabled={working}
                onClick={() => void retryDerivation()}
                className="min-h-10 rounded-md border border-white/10 px-3 text-sm text-zinc-200 hover:bg-white/5 disabled:opacity-50"
              >
                {visibleProfile.derivation_error_code === 'runtime_retry_exhausted' ? t('Profile.retryProfileWork') : t('Profile.retryPreviewGeneration')}
              </button>
            </div>
          ) : null}

          {visibleProfile.candidate ? (
            <div className="mt-5 rounded-xl border border-white/10 bg-black/20 p-4" aria-labelledby="profile-preview-heading">
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div>
                  <h3 id="profile-preview-heading" className="text-sm font-semibold text-white">{t('Profile.generatedPreview')}</h3>
                  {visibleProfile.candidate.source === 'compile_auto' ? (
                    <p className="mt-1 text-xs text-emerald-200">{t('Profile.tagsOnlyUpdate')}</p>
                  ) : null}
                  {visibleProfile.candidate.source === 'manual' ? (
                    <p className="mt-1 text-xs text-zinc-400">
                      {visibleProfile.active?.candidate_id === visibleProfile.candidate.candidate_id
                        ? t('Profile.previewActive')
                        : visibleProfile.confirmed_candidate_id === visibleProfile.candidate.candidate_id
                          ? t('Profile.previewWaitingForTagging')
                          : t('Profile.previewAwaitingConfirmation')}
                    </p>
                  ) : null}
                </div>
                {visibleProfile.candidate.source === 'manual'
                && visibleProfile.confirmed_candidate_id !== visibleProfile.candidate.candidate_id
                && visibleProfile.active?.candidate_id !== visibleProfile.candidate.candidate_id ? (
                  <button
                    type="button"
                    disabled={working}
                    onClick={() => void confirmCandidate()}
                    className="min-h-10 rounded-lg bg-emerald-300 px-3 text-sm font-semibold text-zinc-950 hover:bg-emerald-200 disabled:opacity-50"
                  >
                    {t('Profile.confirmPreview')}
                  </button>
                ) : null}
              </div>

              <div className="mt-4 grid gap-3 md:grid-cols-2">
                <section className="rounded-lg border border-white/10 p-3" aria-labelledby="dictionary-preview-heading">
                  <h4 id="dictionary-preview-heading" className="text-xs font-semibold uppercase tracking-wide text-emerald-200">{t('Profile.dictionaryPreview')}</h4>
                  <p className="mt-2 whitespace-pre-wrap text-sm leading-6 text-zinc-300">{visibleProfile.candidate.preview.dictionary_diff || t('Profile.noDictionaryChanges')}</p>
                </section>
                <section className="rounded-lg border border-white/10 p-3" aria-labelledby="guidance-preview-heading">
                  <h4 id="guidance-preview-heading" className="text-xs font-semibold uppercase tracking-wide text-sky-200">{t('Profile.guidancePreview')}</h4>
                  <p className="mt-2 whitespace-pre-wrap text-sm leading-6 text-zinc-300">
                    {visibleProfile.candidate.source === 'compile_auto'
                      ? t('Profile.guidanceUnchanged')
                      : visibleProfile.candidate.preview.guidance_diff || t('Profile.noGuidanceChanges')}
                  </p>
                </section>
              </div>
              {visibleProfile.candidate.guidance.revision !== visibleProfile.active?.guidance_revision ? (
                <ProfileGuidanceArtifactDetails
                  projectId={projectId}
                  revision={visibleProfile.candidate.guidance.revision}
                  title={t('Profile.candidateWritingGuidance')}
                />
              ) : null}

              {visibleProfile.candidate.preview.requirements.length > 0 ? (
                <div className="mt-4" aria-label={t('Profile.candidateRequirementEffects')}>
                  <h4 className="text-xs font-semibold uppercase tracking-wide text-zinc-300">{t('Profile.requirementEffects')}</h4>
                  <ul className="mt-2 space-y-2">
                    {visibleProfile.candidate.preview.requirements.map((requirement) => (
                      <li key={requirement.id} className="rounded-lg border border-white/10 p-3">
                        <p className="text-sm font-medium text-zinc-200">
                            {requirement.id}: {requirementDispositionLabel(requirement.disposition, t)}
                        </p>
                        <p className="mt-1 text-sm leading-5 text-zinc-400">{requirement.explanation}</p>
                      </li>
                    ))}
                  </ul>
                </div>
              ) : null}

              {visibleProfile.job ? (
                <div className="mt-4 flex flex-wrap items-center gap-x-4 gap-y-2 text-sm text-zinc-300" role="status">
                  <span>{t('Profile.taggingWork', { status: t(`Profile.jobStatus.${visibleProfile.job.status}`) })}</span>
                  {visibleProfile.job.missing_count > 0 ? <span>{t('Profile.itemsIncomplete', { count: visibleProfile.job.missing_count })}</span> : null}
                  {visibleProfile.job.error_code ? <span className="text-amber-200">{visibleProfile.job.error_code}</span> : null}
                  {canRetryTagging ? (
                    <button
                      type="button"
                      disabled={working}
                      onClick={() => void retryTagging()}
                      className="min-h-10 rounded-md border border-white/10 px-3 text-sm text-zinc-200 hover:bg-white/5 disabled:opacity-50"
                    >
                      {t('Profile.retryMissingTagging')}
                    </button>
                  ) : null}
                </div>
              ) : null}
            </div>
          ) : null}

          <div className="mt-6 border-t border-white/10 pt-5">
            <h3 className="text-sm font-semibold text-white">{t('Profile.originalRequirements')}</h3>
            <p className="mt-1 text-xs leading-5 text-zinc-500">{t('Profile.requirementsDescription')}</p>
            <div className="mt-4">
              <ProfileRequirementsEditor
                requirements={draft}
                disabled={saving || working}
                onChange={(next) => {
                  setDraft(next);
                  setError('');
                  setNotice('');
                }}
              />
            </div>
            <button
              type="button"
              disabled={isSavingDisabled}
              onClick={() => void saveRequirements()}
              className="mt-4 min-h-11 rounded-lg bg-emerald-300 px-4 text-sm font-semibold text-zinc-950 transition hover:bg-emerald-200 disabled:cursor-not-allowed disabled:opacity-50"
            >
              {saving ? t('Profile.saving') : t('Profile.saveRequirements')}
            </button>
          </div>
        </>
      ) : null}

      {visibleError ? <p className="mt-4 text-sm text-red-200" role="alert">{visibleError}</p> : null}
      {visibleNotice ? <p className="mt-4 text-sm text-emerald-200" role="status" aria-live="polite">{visibleNotice}</p> : null}
    </section>
  );
}
