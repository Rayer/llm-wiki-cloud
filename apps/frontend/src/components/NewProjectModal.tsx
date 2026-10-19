'use client';

import { FormEvent, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { useRouter } from 'next/navigation';
import { useAuth } from '@/lib/auth';
import { useT } from '@/lib/i18n';
import { useWorkspace } from './WorkspaceProvider';
import { useNavigationBlocker } from './NavigationBlocker';
import { ProfileHowItWorks } from './ProfileHowItWorks';
import type { Project } from '@/lib/projects';

type CreateScope = { ownerId: string; sessionEpoch: number; open: boolean };

export function NewProjectModal() {
  const router = useRouter();
  const { t } = useT();
  const { newProjectOpen, closeNewProject, addProject, user } = useWorkspace();
  const { sessionEpoch } = useAuth();
  const { confirmNavigation } = useNavigationBlocker();
  const [name, setName] = useState('');
  const [loadingScope, setLoadingScope] = useState<CreateScope | null>(null);
  const [error, setError] = useState('');
  const [helpOpen, setHelpOpen] = useState(false);
  const [createdProject, setCreatedProject] = useState<Project | null>(null);
  const nameInputRef = useRef<HTMLInputElement>(null);
  const ownerId = user?.id ?? '';
  const currentScope = useMemo<CreateScope>(
    () => ({ ownerId, sessionEpoch, open: newProjectOpen }),
    [newProjectOpen, ownerId, sessionEpoch],
  );
  const currentScopeRef = useRef<CreateScope | null>(null);
  const previousOwnerId = useRef(`${ownerId}:${sessionEpoch}`);
  const loading = loadingScope === currentScope;
  const closeDialog = useCallback(() => {
    if (loading) return;
    setHelpOpen(false);
    closeNewProject();
  }, [closeNewProject, loading]);

  useLayoutEffect(() => {
    currentScopeRef.current = currentScope;
    return () => {
      if (currentScopeRef.current === currentScope) currentScopeRef.current = null;
    };
  }, [currentScope]);

  useEffect(() => {
    if (previousOwnerId.current === `${ownerId}:${sessionEpoch}`) return;
    previousOwnerId.current = `${ownerId}:${sessionEpoch}`;
    // Account/session changes must not carry another identity's project draft forward.
    setName('');
    setHelpOpen(false);
    setCreatedProject(null);
    setError('');
  }, [ownerId, sessionEpoch]);

  function operationIsCurrent(scope: CreateScope): boolean {
    return currentScopeRef.current === scope;
  }

  useEffect(() => {
    if (!newProjectOpen) return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') closeDialog();
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [closeDialog, newProjectOpen]);

  useEffect(() => {
    if (!helpOpen && newProjectOpen) nameInputRef.current?.focus();
  }, [helpOpen, newProjectOpen]);

  if (!newProjectOpen) return null;

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const trimmedName = name.trim();
    if ((!trimmedName && !createdProject) || !confirmNavigation()) return;
    const scope = currentScope;
    setLoadingScope(scope);
    setError('');
    let project = createdProject;
    try {
      if (!project) {
        project = await addProject(trimmedName);
        if (!operationIsCurrent(scope)) return;
        setCreatedProject(project);
      }
      if (!operationIsCurrent(scope)) return;
      router.push('/profile');
      if (!operationIsCurrent(scope)) return;
      closeNewProject();
      setName('');
      setHelpOpen(false);
      setCreatedProject(null);
    } catch (submitError) {
      if (!operationIsCurrent(scope)) return;
      const message = submitError instanceof Error ? submitError.message : t('NewProject.requestFailed');
      setError(project ? t('NewProject.navigationFailed', { error: message }) : message);
    } finally {
      if (operationIsCurrent(scope)) setLoadingScope(null);
    }
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/75 p-4 backdrop-blur-sm"
      onMouseDown={closeDialog}
    >
      <div
        className={`w-full rounded-2xl border border-white/10 bg-[#151515] p-6 shadow-2xl ${helpOpen
          ? 'flex h-[calc(100dvh-2rem)] max-h-[54rem] max-w-4xl flex-col overflow-hidden'
          : 'max-h-[calc(100dvh-2rem)] max-w-md overflow-y-auto'}`}
        role="dialog"
        aria-modal="true"
        aria-labelledby="new-project-title"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="flex items-start justify-between gap-4">
          <div>
            <h2 id="new-project-title" className="text-2xl font-semibold text-white">
              {t('NewProject.title')}
            </h2>
            <p className="mt-2 text-sm leading-6 text-zinc-400">
              {createdProject ? t('NewProject.createdDescription') : t('NewProject.description')}
            </p>
          </div>
          <button
            type="button"
            onClick={closeDialog}
            disabled={loading}
            className="rounded-md p-2 text-zinc-400 transition hover:bg-white/10 hover:text-white"
            aria-label={t('NewProject.close')}
          >
            ×
          </button>
        </div>

        <form onSubmit={handleSubmit} className="mt-6 space-y-4" hidden={helpOpen}>
          <label className="block text-sm font-medium text-zinc-300">
            {t('NewProject.name')}
            <input
              ref={nameInputRef}
              autoFocus
              required={!createdProject}
              maxLength={80}
              value={name}
              onChange={(event) => setName(event.target.value)}
              disabled={Boolean(createdProject)}
              placeholder={t('NewProject.namePlaceholder')}
              className="mt-2 w-full rounded-lg border border-white/10 bg-black/30 px-4 py-3 text-white outline-none transition placeholder:text-zinc-400 focus:border-emerald-300 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-400"
            />
          </label>
          {!createdProject ? (
            <button
              type="button"
              onClick={() => setHelpOpen(true)}
              disabled={loading}
              className="min-h-11 rounded-lg border border-white/10 px-3 text-sm font-medium text-zinc-200 transition hover:bg-white/5 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-300"
            >
              {t('NewProject.learnAboutPreferences')}
            </button>
          ) : null}
          {error ? <p className="text-sm text-red-300" role="alert">{error}</p> : null}
          <div className="flex justify-end gap-3">
            <button
              type="button"
              onClick={closeDialog}
              disabled={loading}
              className="rounded-lg px-4 py-2.5 text-sm font-medium text-zinc-300 transition hover:bg-white/10 hover:text-white"
            >
              {t('Shell.cancel')}
            </button>
            <button
              type="submit"
              disabled={loading || (!name.trim() && !createdProject)}
              className="rounded-lg bg-emerald-300 px-4 py-2.5 text-sm font-semibold text-black transition hover:bg-emerald-200 disabled:cursor-not-allowed disabled:opacity-60"
            >
              {loading
                ? (createdProject ? t('NewProject.opening') : t('NewProject.creating'))
                : (createdProject ? t('NewProject.openSettings') : t('NewProject.create'))}
            </button>
          </div>
        </form>

        <div className={helpOpen ? 'min-h-0 flex-1 overflow-y-auto overscroll-contain' : 'hidden'}>
          <ProfileHowItWorks
            open={helpOpen}
            mode="creation"
            onBack={() => {
              setHelpOpen(false);
            }}
            onSetPreferences={() => undefined}
            onUseGeneralSettings={() => undefined}
          />
        </div>
      </div>
    </div>
  );
}
