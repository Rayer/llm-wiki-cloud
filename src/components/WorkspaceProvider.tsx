'use client';

import {
  createContext,
  ReactNode,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
} from 'react';
import {
  useAuth,
  type AuthUser,
} from '@/lib/auth';
import {
  createProject,
  getProjects,
  LAST_PROJECT_KEY,
  selectDefaultProject,
  type Project,
} from '@/lib/projects';

type WorkspaceContextValue = {
  hydrated: boolean;
  token: string | null;
  user: AuthUser | null;
  projects: Project[];
  currentProject: Project | null;
  projectsLoading: boolean;
  projectsError: string;
  isDemoSession: boolean;
  loginOpen: boolean;
  newProjectOpen: boolean;
  signIn: (email: string, password: string) => Promise<void>;
  signInAsDemo: (email: string, password: string) => Promise<void>;
  register: (email: string, password: string) => Promise<void>;
  signOut: () => Promise<void>;
  selectProject: (projectId: string) => void;
  addProject: (name: string) => Promise<Project>;
  refreshProjects: () => Promise<void>;
  openNewProject: () => void;
  closeNewProject: () => void;
};

const WorkspaceContext = createContext<WorkspaceContextValue | null>(null);

export function WorkspaceProvider({ children }: { children: ReactNode }) {
  const {
    accessToken: token,
    hydrated,
    user,
    login,
    loginAsDemo,
    register,
    logout,
    isDemoSession,
  } = useAuth();
  const [projects, setProjects] = useState<Project[]>([]);
  const [currentProject, setCurrentProject] = useState<Project | null>(null);
  const [projectsLoading, setProjectsLoading] = useState(false);
  const [projectsError, setProjectsError] = useState('');
  const [newProjectOpen, setNewProjectOpen] = useState(false);
  const loadProjects = useCallback(async () => {
    setProjectsLoading(true);
    setProjectsError('');
    try {
      const nextProjects = await getProjects();
      const selected = selectDefaultProject(
        nextProjects,
        window.localStorage.getItem(LAST_PROJECT_KEY),
      );
      setProjects(nextProjects);
      setCurrentProject(selected);
      if (selected) {
        window.localStorage.setItem(LAST_PROJECT_KEY, selected.id);
      } else {
        window.localStorage.removeItem(LAST_PROJECT_KEY);
      }
    } catch (error) {
      setProjects([]);
      setCurrentProject(null);
      setProjectsError(error instanceof Error ? error.message : 'Unable to load projects.');
    } finally {
      setProjectsLoading(false);
    }
  }, []);

  const signIn = useCallback(async (email: string, password: string) => {
    await login(email, password);
  }, [login]);

  const signInAsDemo = useCallback(async (email: string, password: string) => {
    await loginAsDemo(email, password);
  }, [loginAsDemo]);

  const signOut = useCallback(async () => {
    await logout();
    window.localStorage.removeItem(LAST_PROJECT_KEY);
    setProjects([]);
    setCurrentProject(null);
    setProjectsError('');
    setNewProjectOpen(false);
  }, [logout]);

  const selectProject = useCallback((projectId: string) => {
    const selected = projects.find((project) => project.id === projectId);
    if (!selected) return;
    window.localStorage.setItem(LAST_PROJECT_KEY, selected.id);
    setCurrentProject(selected);
  }, [projects]);

  const addProject = useCallback(async (name: string) => {
    if (!token) throw new Error('Please log in to create a project.');
    if (isDemoSession) throw new Error('Demo mode cannot create projects.');
    const project = await createProject(name);
    setProjects((current) => {
      const withoutDuplicate = current.filter((item) => item.id !== project.id);
      return [...withoutDuplicate, project];
    });
    window.localStorage.setItem(LAST_PROJECT_KEY, project.id);
    setCurrentProject(project);
    setProjectsError('');
    setNewProjectOpen(false);
    return project;
  }, [isDemoSession, token]);

  const refreshProjects = useCallback(async () => {
    if (token) await loadProjects();
  }, [loadProjects, token]);

  useEffect(() => {
    if (!hydrated) return;
    if (token) {
      // eslint-disable-next-line react-hooks/set-state-in-effect -- synchronize project state after auth hydration/token changes
      void loadProjects();
      return;
    }

    setProjects([]);
    setCurrentProject(null);
    setProjectsError('');
  }, [hydrated, loadProjects, token]);

  const value = useMemo<WorkspaceContextValue>(() => ({
    hydrated,
    token,
    user,
    projects,
    currentProject,
    projectsLoading,
    projectsError,
    isDemoSession,
    loginOpen: hydrated && !token,
    newProjectOpen,
    signIn,
    signInAsDemo,
    register,
    signOut,
    selectProject,
    addProject,
    refreshProjects,
    openNewProject: () => {
      if (isDemoSession) return;
      setNewProjectOpen(true);
    },
    closeNewProject: () => setNewProjectOpen(false),
  }), [
    addProject,
    currentProject,
    hydrated,
    isDemoSession,
    newProjectOpen,
    projects,
    projectsError,
    register,
    signIn,
    signInAsDemo,
    projectsLoading,
    refreshProjects,
    selectProject,
    signOut,
    token,
    user,
  ]);

  return <WorkspaceContext.Provider value={value}>{children}</WorkspaceContext.Provider>;
}

export function useWorkspace(): WorkspaceContextValue {
  const value = useContext(WorkspaceContext);
  if (!value) throw new Error('useWorkspace must be used within WorkspaceProvider.');
  return value;
}
