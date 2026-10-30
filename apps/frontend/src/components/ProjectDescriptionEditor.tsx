'use client';

import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { useAuth } from '@/lib/auth';
import { ApiError } from '@/lib/api';
import { useT } from '@/lib/i18n';
import {
  getProjectDescription,
  saveProjectDescription,
  type ProjectDescription,
} from '@/lib/projects';
import {
  isValidProjectDescription,
  normalizeProjectDescription,
  projectDescriptionCodePointCount,
  MAX_PROJECT_DESCRIPTION_LENGTH,
} from '@/lib/project-core';
import { useWorkspace } from './WorkspaceProvider';
import { useNavigationBlocker } from './NavigationBlocker';

type RequestScope = { projectId: string; ownerId: string; sessionEpoch: number; identity: object };

function message(error: unknown, fallback: string): string {
  return error instanceof Error ? error.message : fallback;
}

export function ProjectDescriptionEditor({ projectId }: { projectId: string }) {
  const { t } = useT();
  const { user, sessionEpoch } = useAuth();
  const { updateProjectDescription } = useWorkspace();
  const { setBlocked } = useNavigationBlocker();
  const tRef = useRef(t);
  const [saved, setSaved] = useState<ProjectDescription | null>(null);
  const [draft, setDraft] = useState('');
  const [loadState, setLoadState] = useState<'loading' | 'ready' | 'failed'>('loading');
  const [loadingError, setLoadingError] = useState('');
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState('');
  const [savedNotice, setSavedNotice] = useState(false);
  const [conflict, setConflict] = useState(false);
  const [latest, setLatest] = useState<ProjectDescription | null>(null);
  const [latestLoading, setLatestLoading] = useState(false);
  const [latestError, setLatestError] = useState('');
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const sequenceRef = useRef(0);
  const mountedRef = useRef(false);
  const blockerOwnerRef = useRef({});
  const currentScope = useMemo<RequestScope>(
    () => ({ projectId, ownerId: user?.id ?? '', sessionEpoch, identity: {} }),
    [projectId, sessionEpoch, user?.id],
  );
  const currentScopeRef = useRef<RequestScope | null>(null);

  useEffect(() => { tRef.current = t; }, [t]);

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

  const requestIsCurrent = useCallback((scope: RequestScope, sequence: number) => (
    mountedRef.current
    && currentScopeRef.current === scope
    && sequenceRef.current === sequence
  ), []);

  const load = useCallback(async () => {
    const scope = currentScope;
    const sequence = ++sequenceRef.current;
    try {
      const result = await getProjectDescription(scope.projectId);
      if (!requestIsCurrent(scope, sequence)) return;
      if (result.projectId !== scope.projectId) throw new Error(tRef.current('ProjectDescription.responseMismatch'));
      setSaved(result);
      setDraft(result.description);
      setLoadState('ready');
    } catch (error: unknown) {
      if (!requestIsCurrent(scope, sequence)) return;
      setLoadState('failed');
      setLoadingError(message(error, tRef.current('ProjectDescription.loadFailed')));
    }
  }, [currentScope, requestIsCurrent]);

  useEffect(() => {
    const scope = currentScope;
    const sequence = ++sequenceRef.current;
    void getProjectDescription(scope.projectId)
      .then((result) => {
        if (!requestIsCurrent(scope, sequence)) return;
        if (result.projectId !== scope.projectId) throw new Error(tRef.current('ProjectDescription.responseMismatch'));
        setSaved(result);
        setDraft(result.description);
        setLoadState('ready');
      })
      .catch((error: unknown) => {
        if (!requestIsCurrent(scope, sequence)) return;
        setLoadState('failed');
        setLoadingError(message(error, tRef.current('ProjectDescription.loadFailed')));
      });
    return () => { sequenceRef.current += 1; };
  }, [currentScope, requestIsCurrent]);

  const retryLoad = useCallback(() => {
    setLoadState('loading');
    setLoadingError('');
    setSaved(null);
    setDraft('');
    setSaveError('');
    setSavedNotice(false);
    setConflict(false);
    setLatest(null);
    void load();
  }, [load]);

  useEffect(() => {
    if (loadState === 'ready' && typeof window !== 'undefined' && window.location.hash === '#project-description') {
      textareaRef.current?.focus();
    }
  }, [loadState]);

  const normalizedDraft = normalizeProjectDescription(draft);
  const dirty = Boolean(saved && normalizedDraft !== saved.description);
  const valid = isValidProjectDescription(draft);

  useEffect(() => {
    const blockerOwner = blockerOwnerRef.current;
    setBlocked(dirty || saving, blockerOwner);
    return () => setBlocked(false, blockerOwner);
  }, [dirty, saving, setBlocked]);

  const refreshLatest = useCallback(async () => {
    const scope = currentScope;
    const sequence = ++sequenceRef.current;
    setLatestLoading(true);
    setLatestError('');
    try {
      const result = await getProjectDescription(scope.projectId);
      if (!requestIsCurrent(scope, sequence)) return;
      setLatest(result);
      if (result.description === normalizedDraft) {
        setSaved(result);
        setDraft(result.description);
        updateProjectDescription(scope.projectId, result.description, result.revision);
        setConflict(false);
        setSavedNotice(true);
      }
    } catch (error: unknown) {
      if (requestIsCurrent(scope, sequence)) setLatestError(message(error, tRef.current('ProjectDescription.loadFailed')));
    } finally {
      if (requestIsCurrent(scope, sequence)) setLatestLoading(false);
    }
  }, [currentScope, normalizedDraft, requestIsCurrent, updateProjectDescription]);

  const save = useCallback(async () => {
    if (!saved || !dirty || !valid || saving || conflict) return;
    const scope = currentScope;
    const sequence = ++sequenceRef.current;
    const submittedDescription = normalizedDraft;
    setSaving(true);
    setSaveError('');
    setSavedNotice(false);
    setLatest(null);
    setLatestError('');
    try {
      const result = await saveProjectDescription(scope.projectId, submittedDescription, saved.revision);
      if (!requestIsCurrent(scope, sequence)) return;
      if (result.projectId !== scope.projectId) throw new Error(tRef.current('ProjectDescription.responseMismatch'));
      setSaved(result);
      setDraft(result.description);
      updateProjectDescription(scope.projectId, result.description, result.revision);
      setConflict(false);
      setSavedNotice(true);
    } catch (error: unknown) {
      if (!requestIsCurrent(scope, sequence)) return;
      if (error instanceof ApiError && error.status === 409) {
        setConflict(true);
        try {
          const current = await getProjectDescription(scope.projectId);
          if (!requestIsCurrent(scope, sequence)) return;
          if (current.description === submittedDescription) {
            setSaved(current);
            setDraft(current.description);
            updateProjectDescription(scope.projectId, current.description, current.revision);
            setConflict(false);
            setSavedNotice(true);
          } else {
            setLatest(current);
          }
        } catch (readError: unknown) {
          if (requestIsCurrent(scope, sequence)) setLatestError(message(readError, tRef.current('ProjectDescription.loadFailed')));
        }
      } else {
        setSaveError(message(error, tRef.current('ProjectDescription.saveFailed')));
      }
    } finally {
      if (requestIsCurrent(scope, sequence)) setSaving(false);
    }
  }, [conflict, currentScope, dirty, normalizedDraft, requestIsCurrent, saved, saving, updateProjectDescription, valid]);

  function useLatestAsBase() {
    if (!latest) return;
    setSaved(latest);
    updateProjectDescription(projectId, latest.description, latest.revision);
    setConflict(false);
    setLatest(null);
    setLatestError('');
    setSavedNotice(false);
  }

  return (
    <section
      id="project-description"
      aria-labelledby="project-description-heading"
      className="rounded-[var(--radius-lg)] border border-white/10 bg-zinc-900/45 p-5 backdrop-blur-sm sm:p-6"
    >
      <h2 id="project-description-heading" className="text-xl font-semibold text-white">
        {t('ProjectDescription.title')}
      </h2>
      <p className="mt-2 max-w-3xl text-sm leading-6 text-zinc-400">{t('ProjectDescription.help')}</p>

      {loadState === 'loading' ? (
        <p className="mt-4 text-sm text-zinc-400" role="status">{t('ProjectDescription.loading')}</p>
      ) : null}
      {loadState === 'failed' ? (
        <div className="mt-4" role="alert">
          <p className="text-sm text-red-200">{t('ProjectDescription.loadFailed', { error: loadingError })}</p>
          <button type="button" onClick={retryLoad} className="mt-2 min-h-10 rounded-md border border-white/10 px-3 text-sm text-zinc-200 hover:bg-white/5">
            {t('ProjectDescription.retry')}
          </button>
        </div>
      ) : null}

      {loadState === 'ready' && saved ? (
        <>
          <div className="mt-4 rounded-lg border border-white/8 bg-black/15 p-3">
            <p className="text-xs font-medium uppercase tracking-wide text-zinc-500">{t('ProjectDescription.savedValue')}</p>
            <p className="mt-2 whitespace-pre-wrap break-words text-sm leading-6 text-zinc-200">
              {saved.description || t('ProjectDescription.empty')}
            </p>
          </div>
          <label htmlFor="project-description-input" className="mt-4 block text-sm font-medium text-zinc-300">
            {t('ProjectDescription.label')}
          </label>
          <textarea
            ref={textareaRef}
            id="project-description-input"
            rows={5}
            value={draft}
            disabled={saving}
            onChange={(event) => {
              setDraft(event.target.value);
              setSaveError('');
              setSavedNotice(false);
            }}
            className="mt-2 w-full resize-y rounded-lg border border-white/10 bg-black/30 px-4 py-3 text-white outline-none transition focus:border-emerald-300 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-400 disabled:opacity-60"
          />
          <p className="mt-2 text-xs text-zinc-500" aria-live="polite">
            {t('ProjectDescription.counter', { count: projectDescriptionCodePointCount(draft), max: MAX_PROJECT_DESCRIPTION_LENGTH })}
          </p>
          <p className="mt-1 text-xs leading-5 text-zinc-500">{t('ProjectDescription.example')}</p>
          {!valid ? <p className="mt-2 text-sm text-amber-200" role="alert">{t('ProjectDescription.invalid')}</p> : null}
          {saveError ? <p className="mt-3 text-sm text-red-200" role="alert">{t('ProjectDescription.saveFailed', { error: saveError })}</p> : null}
          {savedNotice ? <p className="mt-3 text-sm text-emerald-200" role="status">{t('ProjectDescription.saved')}</p> : null}
          {conflict ? (
            <div className="mt-3 rounded-lg border border-amber-200/30 bg-amber-200/[0.06] p-3" role="alert">
              <p className="text-sm text-amber-100">{t('ProjectDescription.conflict')}</p>
              {latest ? (
                <>
                  <p className="mt-2 text-xs font-medium uppercase tracking-wide text-zinc-400">{t('ProjectDescription.latestValue')}</p>
                  <p className="mt-1 whitespace-pre-wrap break-words text-sm leading-6 text-zinc-200">{latest.description || t('ProjectDescription.empty')}</p>
                </>
              ) : null}
              {latestError ? <p className="mt-2 text-sm text-amber-100">{t('ProjectDescription.latestFailed', { error: latestError })}</p> : null}
              {latest ? (
                <button type="button" onClick={useLatestAsBase} className="mt-3 min-h-10 rounded-md border border-amber-100/20 px-3 text-sm text-amber-50 hover:bg-amber-100/5">
                  {t('ProjectDescription.useLatest')}
                </button>
              ) : (
                <button type="button" onClick={() => void refreshLatest()} disabled={latestLoading} className="mt-3 min-h-10 rounded-md border border-amber-100/20 px-3 text-sm text-amber-50 hover:bg-amber-100/5 disabled:opacity-60">
                  {latestLoading ? t('ProjectDescription.loadingLatest') : t('ProjectDescription.loadLatest')}
                </button>
              )}
            </div>
          ) : null}
          <div className="mt-4 flex flex-wrap items-center gap-3">
            <button
              type="button"
              onClick={() => void save()}
              disabled={!dirty || !valid || saving || conflict}
              className="min-h-11 rounded-lg bg-emerald-300 px-4 py-2 text-sm font-semibold text-zinc-950 transition hover:bg-emerald-200 disabled:cursor-not-allowed disabled:opacity-50"
            >
              {saving ? t('ProjectDescription.saving') : t('ProjectDescription.save')}
            </button>
            {saved.description === '' ? <p className="text-sm text-zinc-500">{t('ProjectDescription.empty')}</p> : null}
          </div>
        </>
      ) : null}
    </section>
  );
}
