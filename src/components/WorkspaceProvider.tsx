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
  getStoredToken,
  getStoredUser,
  login,
  logout,
  AUTH_TOKEN_KEY,
  AUTH_USER_KEY,
  type AuthUser,
  type LoginResponse,
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
  loginOpen: boolean;
  newProjectOpen: boolean;
  isDemo: boolean;
  signIn: (email: string, password: string) => Promise<void>;
  registerUser: (token: string, user: AuthUser) => void;
  demoSignIn: () => void;
  signOut: () => void;
  selectProject: (projectId: string) => void;
  addProject: (name: string) => Promise<Project>;
  refreshProjects: () => Promise<void>;
  openNewProject: () => void;
  closeNewProject: () => void;
};

const WorkspaceContext = createContext<WorkspaceContextValue | null>(null);

export function WorkspaceProvider({ children }: { children: ReactNode }) {
  const [hydrated, setHydrated] = useState(false);
  const [token, setToken] = useState<string | null>(null);
  const [user, setUser] = useState<AuthUser | null>(null);
  const [projects, setProjects] = useState<Project[]>([]);
  const [currentProject, setCurrentProject] = useState<Project | null>(null);
  const [projectsLoading, setProjectsLoading] = useState(false);
  const [projectsError, setProjectsError] = useState('');
  const [newProjectOpen, setNewProjectOpen] = useState(false);

  const loadProjects = useCallback(async (authToken: string) => {
    setProjectsLoading(true);
    setProjectsError('');
    try {
      const nextProjects = await getProjects(authToken);
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

  useEffect(() => {
    const storedToken = getStoredToken();
    const storedUser = getStoredUser();
    // eslint-disable-next-line react-hooks/set-state-in-effect -- hydrate browser-only storage after SSR
    setToken(storedToken);
    setUser(storedUser);
    setHydrated(true);
    if (storedToken) void loadProjects(storedToken);
  }, [loadProjects]);

  const signIn = useCallback(async (email: string, password: string) => {
    const result: LoginResponse = await login(email, password);
    setToken(result.token);
    setUser(result.user);
    await loadProjects(result.token);
  }, [loadProjects]);

  const signOut = useCallback(() => {
    logout();
    window.localStorage.removeItem(LAST_PROJECT_KEY);
    setToken(null);
    setUser(null);
    setProjects([]);
    setCurrentProject(null);
    setProjectsError('');
    setNewProjectOpen(false);
  }, []);

  const registerUser = useCallback((token: string, u: AuthUser) => {
    window.localStorage.setItem(AUTH_TOKEN_KEY, token);
    window.localStorage.setItem(AUTH_USER_KEY, JSON.stringify(u));
    setToken(token);
    setUser(u);
  }, []);

  const demoSignIn = useCallback(() => {
    const demoToken = 'demo-token';
    const demoUser: AuthUser = { id: 'test-user', email: 'demo@llm.wiki' };
    window.localStorage.setItem(AUTH_TOKEN_KEY, demoToken);
    window.localStorage.setItem(AUTH_USER_KEY, JSON.stringify(demoUser));
    const demoProject: Project = { id: 'demo', name: 'Demo (Lifestyle Wiki)' };
    setToken(demoToken);
    setUser(demoUser);
    setProjects([demoProject]);
    setCurrentProject(demoProject);
    window.localStorage.setItem(LAST_PROJECT_KEY, demoProject.id);
    setProjectsError('');
  }, []);

  const selectProject = useCallback((projectId: string) => {
    const selected = projects.find((project) => project.id === projectId);
    if (!selected) return;
    window.localStorage.setItem(LAST_PROJECT_KEY, selected.id);
    setCurrentProject(selected);
  }, [projects]);

  const addProject = useCallback(async (name: string) => {
    if (!token) throw new Error('Please log in to create a project.');
    const project = await createProject(name, token);
    setProjects((current) => {
      const withoutDuplicate = current.filter((item) => item.id !== project.id);
      return [...withoutDuplicate, project];
    });
    window.localStorage.setItem(LAST_PROJECT_KEY, project.id);
    setCurrentProject(project);
    setProjectsError('');
    setNewProjectOpen(false);
    return project;
  }, [token]);

  const refreshProjects = useCallback(async () => {
    if (token) await loadProjects(token);
  }, [loadProjects, token]);

  const value = useMemo<WorkspaceContextValue>(() => ({
    hydrated,
    token,
    user,
    projects,
    currentProject,
    projectsLoading,
    projectsError,
    loginOpen: hydrated && !token,
    newProjectOpen,
    isDemo: token === 'demo-token',
    signIn,
    registerUser,
    demoSignIn,
    signOut,
    selectProject,
    addProject,
    refreshProjects,
    openNewProject: () => setNewProjectOpen(true),
    closeNewProject: () => setNewProjectOpen(false),
  }), [
    addProject,
    currentProject,
    demoSignIn,
    hydrated,
    newProjectOpen,
    projects,
    registerUser,
    signIn,
    projectsLoading,
    refreshProjects,
    selectProject,
    signIn,
    demoSignIn,
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
