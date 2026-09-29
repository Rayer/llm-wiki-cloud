'use client';

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
      <h1 className="font-serif text-2xl font-medium text-[#eeeae4]">{t('Profile.pageTitle')}</h1>
      <ProjectProfilePanel
        key={`${user.id}:${currentProject.id}`}
        projectId={currentProject.id}
      />
    </div>
  );
}
