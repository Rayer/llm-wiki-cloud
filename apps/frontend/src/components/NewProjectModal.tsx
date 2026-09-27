'use client';

import { FormEvent, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { useRouter } from 'next/navigation';
import { useAuth } from '@/lib/auth';
import { useWorkspace } from './WorkspaceProvider';
import { useNavigationBlocker } from './NavigationBlocker';
import { getProfile, saveProfile, type ProfileRequirement } from '@/lib/api';
import type { Project } from '@/lib/projects';
import { ProfileRequirementsEditor } from './ProfileRequirementsEditor';

function sameRequirements(left: ProfileRequirement[], right: ProfileRequirement[]): boolean {
  return left.length === right.length
    && left.every((item, index) => item.id === right[index]?.id && item.text === right[index]?.text);
}

type CreateScope = { ownerId: string; sessionEpoch: number; open: boolean };

export function NewProjectModal() {
  const router = useRouter();
  const { newProjectOpen, closeNewProject, addProject, user } = useWorkspace();
  const { sessionEpoch } = useAuth();
  const { confirmNavigation } = useNavigationBlocker();
  const [name, setName] = useState('');
  const [loadingScope, setLoadingScope] = useState<CreateScope | null>(null);
  const [error, setError] = useState('');
  const [requirements, setRequirements] = useState<ProfileRequirement[]>([]);
  const [createdProject, setCreatedProject] = useState<Project | null>(null);
  const ownerId = user?.id ?? '';
  const currentScope = useMemo<CreateScope>(
    () => ({ ownerId, sessionEpoch, open: newProjectOpen }),
    [newProjectOpen, ownerId, sessionEpoch],
  );
  const currentScopeRef = useRef<CreateScope | null>(null);
  const previousOwnerId = useRef(`${ownerId}:${sessionEpoch}`);
  const loading = loadingScope === currentScope;

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
    setRequirements([]);
    setCreatedProject(null);
    setError('');
  }, [ownerId, sessionEpoch]);

  function operationIsCurrent(scope: CreateScope): boolean {
    return currentScopeRef.current === scope;
  }

  useEffect(() => {
    if (!newProjectOpen) return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && !loading) closeNewProject();
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [closeNewProject, loading, newProjectOpen]);

  if (!newProjectOpen) return null;

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const trimmedName = name.trim();
    if ((!trimmedName && !createdProject) || !confirmNavigation()) return;
    const scope = currentScope;
    setLoadingScope(scope);
    setError('');
    let project = createdProject;
    let profileSaveAttempted = false;
    try {
      if (!project) {
        project = await addProject(trimmedName);
        if (!operationIsCurrent(scope)) return;
        setCreatedProject(project);
      }
      const requirementsToSave = requirements;
      if (requirementsToSave.length > 0 || createdProject) {
        profileSaveAttempted = true;
        if (createdProject) {
          const latest = await getProfile(project.id);
          if (!operationIsCurrent(scope)) return;
          if (latest.project_id !== project.id) throw new Error('Profile response does not match the created project.');
          if (!sameRequirements(latest.requirements, requirementsToSave)) {
            const saved = await saveProfile(project.id, latest.revision, requirementsToSave);
            if (!operationIsCurrent(scope)) return;
            if (saved.project_id !== project.id) throw new Error('Profile save response does not match the created project.');
          }
        } else if (requirementsToSave.length > 0) {
          const saved = await saveProfile(project.id, 0, requirementsToSave);
          if (!operationIsCurrent(scope)) return;
          if (saved.project_id !== project.id) throw new Error('Profile save response does not match the created project.');
        }
      }
      if (!operationIsCurrent(scope)) return;
      setLoadingScope(null);
      closeNewProject();
      setName('');
      setRequirements([]);
      setCreatedProject(null);
      router.push('/');
    } catch (submitError) {
      if (!operationIsCurrent(scope)) return;
      const message = submitError instanceof Error ? submitError.message : 'The request could not be completed.';
      setError(project && profileSaveAttempted
        ? `Project created, but Profile save was not confirmed. Your draft is kept; retrying will reuse this project. ${message}`
        : message);
    } finally {
      if (operationIsCurrent(scope)) setLoadingScope(null);
    }
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/75 p-4 backdrop-blur-sm"
      onMouseDown={() => {
        if (!loading) closeNewProject();
      }}
    >
      <div
        className="max-h-[calc(100dvh-2rem)] w-full max-w-md overflow-y-auto rounded-2xl border border-white/10 bg-[#151515] p-6 shadow-2xl"
        role="dialog"
        aria-modal="true"
        aria-labelledby="new-project-title"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="flex items-start justify-between gap-4">
          <div>
            <h2 id="new-project-title" className="text-2xl font-semibold text-white">
              Create project
            </h2>
            <p className="mt-2 text-sm text-zinc-400">
              {createdProject ? 'The project exists. Save the Profile draft here without creating another project.' : 'Start a separate knowledge workspace.'}
            </p>
          </div>
          <button
            type="button"
            onClick={closeNewProject}
            disabled={loading}
            className="rounded-md p-2 text-zinc-400 transition hover:bg-white/10 hover:text-white"
            aria-label="Close create project dialog"
          >
            ×
          </button>
        </div>

        <form onSubmit={handleSubmit} className="mt-6 space-y-4">
          <label className="block text-sm font-medium text-zinc-300">
            Project name
            <input
              autoFocus
              required={!createdProject}
              maxLength={80}
              value={name}
              onChange={(event) => setName(event.target.value)}
              disabled={Boolean(createdProject)}
              placeholder="Product research"
              className="mt-2 w-full rounded-lg border border-white/10 bg-black/30 px-4 py-3 text-white outline-none transition placeholder:text-zinc-400 focus:border-emerald-300 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-400"
            />
          </label>
          <div className="space-y-2 rounded-xl border border-white/10 bg-black/20 p-4">
            <div>
              <h3 className="text-sm font-semibold text-white">Optional Profile requirements</h3>
              <p className="mt-1 text-xs leading-5 text-zinc-400">Leave every field blank to create the project without scheduling Profile work.</p>
            </div>
            <ProfileRequirementsEditor requirements={requirements} onChange={setRequirements} disabled={loading} />
          </div>
          {error ? <p className="text-sm text-red-300">{error}</p> : null}
          <div className="flex justify-end gap-3">
            <button
              type="button"
              onClick={closeNewProject}
              disabled={loading}
              className="rounded-lg px-4 py-2.5 text-sm font-medium text-zinc-300 transition hover:bg-white/10 hover:text-white"
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={loading || (!name.trim() && !createdProject)}
              className="rounded-lg bg-emerald-300 px-4 py-2.5 text-sm font-semibold text-black transition hover:bg-emerald-200 disabled:cursor-not-allowed disabled:opacity-60"
            >
              {loading ? (createdProject ? 'Saving Profile…' : 'Creating…') : (createdProject ? 'Retry Profile save' : 'Create project')}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}
