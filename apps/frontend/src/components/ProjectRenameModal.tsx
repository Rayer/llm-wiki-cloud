'use client';

import { FormEvent, useState } from 'react';
import { MAX_PROJECT_NAME_LENGTH, type Project } from '@/lib/projects';

type ProjectRenameModalProps = {
  project: Project;
  onSubmit: (name: string) => Promise<void>;
  onClose: () => void;
};

function validateProjectName(name: string): string {
  const trimmedName = name.trim();
  if (!trimmedName) return 'Project name is required.';
  if ([...trimmedName].length > MAX_PROJECT_NAME_LENGTH) {
    return `Project name must be 1-${MAX_PROJECT_NAME_LENGTH} characters.`;
  }
  return '';
}

export function ProjectRenameModal({ project, onSubmit, onClose }: ProjectRenameModalProps) {
  const [name, setName] = useState(project.name);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);

  const validationError = validateProjectName(name);
  const isDuplicate = name.trim() === project.name.trim();
  const submitDisabled = loading || !!validationError || isDuplicate;
  const hasServerError = error !== '';

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (submitDisabled) return;
    setLoading(true);
    setError('');
    try {
      await onSubmit(name.trim());
      onClose();
    } catch (submitError) {
      setError(submitError instanceof Error ? submitError.message : 'Unable to rename project.');
    } finally {
      setLoading(false);
    }
  };

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/75 p-4 backdrop-blur-sm"
      onMouseDown={() => {
        if (!loading) onClose();
      }}
    >
      <div
        className="w-full max-w-md rounded-2xl border border-white/10 bg-[#151515] p-6 shadow-2xl"
        role="dialog"
        aria-modal="true"
        aria-labelledby="rename-project-title"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="flex items-start justify-between gap-4">
          <h2 id="rename-project-title" className="text-2xl font-semibold text-white">
            Rename project
          </h2>
          <button
            type="button"
            onClick={onClose}
            disabled={loading}
            className="rounded-md p-2 text-zinc-400 transition hover:bg-white/10 hover:text-white"
            aria-label="Close rename project dialog"
          >
            ×
          </button>
        </div>

        <form onSubmit={handleSubmit} className="mt-6 space-y-4">
          <label className="block text-sm font-medium text-zinc-300">
            Project name
            <input
              autoFocus
              required
              value={name}
              onChange={(event) => {
                setName(event.target.value);
                if (hasServerError) setError('');
              }}
              placeholder="Project name"
              className="mt-2 w-full rounded-lg border border-white/10 bg-black/30 px-4 py-3 text-white outline-none transition placeholder:text-zinc-400 focus:border-emerald-300 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-400"
            />
          </label>
          {validationError || hasServerError ? (
            <p className="text-sm text-red-300">{hasServerError ? error : validationError}</p>
          ) : null}
          <div className="flex justify-end gap-3">
            <button
              type="button"
              onClick={() => onClose()}
              disabled={loading}
              className="rounded-lg px-4 py-2.5 text-sm font-medium text-zinc-300 transition hover:bg-white/10 hover:text-white"
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={submitDisabled}
              className="rounded-lg bg-emerald-300 px-4 py-2.5 text-sm font-semibold text-black transition hover:bg-emerald-200 disabled:cursor-not-allowed disabled:opacity-60"
            >
              {loading ? 'Renaming…' : 'Rename'}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}
