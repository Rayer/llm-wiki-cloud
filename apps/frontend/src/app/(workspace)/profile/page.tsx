'use client';

import { useState } from 'react';
import { ProjectRenameModal } from '@/components/ProjectRenameModal';
import type { Project } from '@/lib/projects';
import { ProjectProfilePanel } from '@/components/ProjectProfilePanel';
import { useWorkspace } from '@/components/WorkspaceProvider';
import { useAuth } from '@/lib/auth';
import { useT } from '@/lib/i18n';

export default function ProfilePage() {
  const { t } = useT();
  const { user } = useAuth();
  const { hydrated, currentProject, isDemoSession, projectsLoading } = useWorkspace();

  if (!hydrated || projectsLoading) {
    return <p role="status" className="text-sm text-zinc-400">{t('Profile.loading')}</p>;
  }
  if (!user) {
    return <p className="text-sm text-zinc-400">{t('Profile.signInRequired')}</p>;
  }
  if (isDemoSession) {
    return <p className="text-sm text-zinc-400">{t('Profile.demoDisabled')}</p>;
  }
  if (!currentProject) {
    return <p className="text-sm text-zinc-400">{t('Profile.selectProject')}</p>;
  }

  return (
    <div className="space-y-5">
      <ProjectHeading key={`heading:${user.id}:${currentProject.id}`} project={currentProject} />
      <ProjectProfilePanel
        key={`${user.id}:${currentProject.id}`}
        projectId={currentProject.id}
      />
    </div>
  );
}

function ProjectHeading({ project }: { project: Project }) {
  const { t } = useT();
  const { renameProject } = useWorkspace();
  const [renaming, setRenaming] = useState(false);

  return (
    <div className="flex flex-wrap items-center justify-between gap-3">
      <div>
        <h1 className="font-serif text-2xl font-medium text-[#eeeae4]">{t('Profile.pageTitle')}</h1>
        <p className="mt-1 break-words text-sm text-zinc-400">{project.name}</p>
      </div>
      <button
        type="button"
        onClick={() => setRenaming(true)}
        className="min-h-11 rounded-lg border border-white/10 px-4 py-2 text-sm text-zinc-300 transition hover:bg-white/5 hover:text-white"
      >
        {t('Profile.renameProject')}
      </button>
      {renaming ? (
        <ProjectRenameModal
          project={project}
          onSubmit={async (name) => { await renameProject(project.id, name); }}
          provisional={project.name === 'Default Project'}
          onClose={() => setRenaming(false)}
        />
      ) : null}
    </div>
  );
}
