'use client';

import {
  createContext,
  ReactNode,
  useCallback,
  useContext,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from 'react';
import { useRouter } from 'next/navigation';
import {
  useAuth,
  type AuthUser,
} from '@/lib/auth';
import {
  clearForceHomeRedirect,
  consumeForceHomeRedirect,
} from '@/lib/auth-core';
import { getDocumentImportFailureSummary, getStatus } from '@/lib/api';
import { clearCompletedQueryResults } from '@/lib/query-result-history';
import {
  bootstrapDefaultProject,
  createProject,
  renameProject as renameProjectRequest,
  getProjects,
  LAST_PROJECT_KEY,
  selectDefaultProject,
  type Project,
} from '@/lib/projects';
import { useNavigationBlocker } from './NavigationBlocker';

export type NavCounts = {
  sources: number | null;
  concepts: number | null;
  documentImportFailures: number | null;
};

type ProfileOnboardingIntent = {
  ownerId: string;
  projectId: string;
  sessionEpoch: number;
};

const PROFILE_ONBOARDING_PENDING_KEY = 'llm-wiki-profile-onboarding-pending';
const profileOnboardingPresentedMemory = new Set<string>();

function profileOnboardingPresentedKey(ownerId: string, projectId: string): string {
  return `llm-wiki-profile-onboarding-presented:${encodeURIComponent(ownerId)}:${encodeURIComponent(projectId)}`;
}

function readLastProjectId(): string | null {
  try {
    return window.localStorage.getItem(LAST_PROJECT_KEY);
  } catch {
    return null;
  }
}

function writeLastProjectId(projectId: string | null): void {
  try {
    if (projectId) window.localStorage.setItem(LAST_PROJECT_KEY, projectId);
    else window.localStorage.removeItem(LAST_PROJECT_KEY);
  } catch {
    // Project selection remains usable when browser storage is unavailable.
  }
}

function clearPendingProfileOnboarding(): void {
  try {
    window.sessionStorage.removeItem(PROFILE_ONBOARDING_PENDING_KEY);
  } catch {
    // The in-memory intent remains scoped to the current provider session.
  }
}

function writePendingProfileOnboarding(intent: ProfileOnboardingIntent): void {
  try {
    window.sessionStorage.setItem(
      PROFILE_ONBOARDING_PENDING_KEY,
      JSON.stringify({ ownerId: intent.ownerId, projectId: intent.projectId }),
    );
  } catch {
    // The in-memory intent covers this tab when session storage is restricted.
  }
}

function readPendingProfileOnboarding(ownerId: string, sessionEpoch: number): ProfileOnboardingIntent | null {
  try {
    const value: unknown = JSON.parse(window.sessionStorage.getItem(PROFILE_ONBOARDING_PENDING_KEY) ?? 'null');
    if (
      value && typeof value === 'object'
      && 'ownerId' in value && value.ownerId === ownerId
      && 'projectId' in value && typeof value.projectId === 'string' && value.projectId
    ) {
      return { ownerId, projectId: value.projectId, sessionEpoch };
    }
  } catch {
    // A missing or blocked session store does not affect project loading.
  }
  return null;
}

function profileOnboardingWasPresented(ownerId: string, projectId: string): boolean {
  const key = profileOnboardingPresentedKey(ownerId, projectId);
  if (profileOnboardingPresentedMemory.has(key)) return true;
  try {
    return window.localStorage.getItem(key) === '1';
  } catch {
    return false;
  }
}

const emptyNavCounts: NavCounts = {
  sources: null,
  concepts: null,
  documentImportFailures: null,
};

function consumeExpiredLoginRedirect(router: { replace: (href: string) => void }): void {
  if (typeof window === 'undefined' || !consumeForceHomeRedirect(window.localStorage)) return;
  if (
    window.location.pathname !== '/'
    || window.location.search !== ''
    || window.location.hash !== ''
  ) {
    router.replace('/');
  }
}

type WorkspaceContextValue = {
  hydrated: boolean;
  token: string | null;
  user: AuthUser | null;
  sessionEpoch: number;
  projects: Project[];
  currentProject: Project | null;
  projectsLoading: boolean;
  projectsError: string;
  productSetupPending: boolean;
  isDemoSession: boolean;
  loginOpen: boolean;
  newProjectOpen: boolean;
  profileOnboarding: ProfileOnboardingIntent | null;
  markProfileOnboardingPresented: (ownerId: string, projectId: string, sessionEpoch: number) => boolean;
  /** Live status counts for sidebar badges; null fields mean unavailable / no project. */
  navCounts: NavCounts;
  refreshNavCounts: () => Promise<void>;
  signIn: (email: string, password: string) => Promise<void>;
  signInAsDemo: () => Promise<void>;
  register: (email: string, password: string) => Promise<void>;
  signOut: () => Promise<void>;
  selectProject: (projectId: string, destination?: string) => void;
  addProject: (name: string, description?: string, idempotencyKey?: string) => Promise<Project>;
  renameProject: (projectId: string, name: string) => Promise<string>;
  updateProjectDescription: (projectId: string, description: string, revision: number) => void;
  refreshProjects: () => Promise<void>;
  openNewProject: () => void;
  closeNewProject: () => void;
};

const WorkspaceContext = createContext<WorkspaceContextValue | null>(null);

export function WorkspaceProvider({ children }: { children: ReactNode }) {
  const router = useRouter();
  const { confirmNavigation } = useNavigationBlocker();
  const {
    accessToken: token,
    hydrated,
    user,
    login,
    loginAsDemo,
    register,
    logout,
    isDemoSession,
    sessionEpoch,
  } = useAuth();
  const [projects, setProjects] = useState<Project[]>([]);
  const [currentProject, setCurrentProject] = useState<Project | null>(null);
  const [projectsLoading, setProjectsLoading] = useState(false);
  const [projectsError, setProjectsError] = useState('');
  const [productSetupPending, setProductSetupPending] = useState(false);
  const [newProjectOpen, setNewProjectOpen] = useState(false);
  const [profileOnboarding, setProfileOnboarding] = useState<ProfileOnboardingIntent | null>(null);
  const [navCounts, setNavCounts] = useState<NavCounts>(emptyNavCounts);
  const requestIdentityRef = useRef({ projectId: currentProject?.id ?? null, token, sessionEpoch });
  const projectStateActiveRef = useRef(false);
  const projectListGenerationRef = useRef(0);
  const activeProjectLoadRef = useRef<{ token: string; sessionEpoch: number; generation: number } | null>(null);
  const restartProjectLoadRef = useRef(false);
  const autoLoadIdentityRef = useRef<{ token: string | null; ownerId: string | null; sessionEpoch: number } | null>(null);
  const setupReadyOwnerRef = useRef<string | null>(null);
  const bootstrapFlightsRef = useRef(new Map<string, Promise<string>>());
  const renameGenerationByProjectRef = useRef(new Map<string, number>());
  const profileOnboardingOwnerRef = useRef<string | null>(null);
  const queryResultScopeRef = useRef({
    ownerId: token ? user?.id ?? null : null,
    projectId: currentProject?.id ?? null,
  });

  useLayoutEffect(() => {
    requestIdentityRef.current = { projectId: currentProject?.id ?? null, token, sessionEpoch };
  }, [currentProject?.id, sessionEpoch, token]);

  useEffect(() => {
    const renameGenerations = renameGenerationByProjectRef.current;
    projectStateActiveRef.current = true;
    return () => {
      projectStateActiveRef.current = false;
      projectListGenerationRef.current += 1;
      const activeLoad = activeProjectLoadRef.current;
      if (activeLoad?.token === token && activeLoad.sessionEpoch === sessionEpoch) {
        restartProjectLoadRef.current = true;
        activeProjectLoadRef.current = null;
      }
      renameGenerations.clear();
    };
  }, [sessionEpoch, token]);

  useEffect(() => {
    const previous = queryResultScopeRef.current;
    const next = {
      ownerId: token ? user?.id ?? null : null,
      projectId: currentProject?.id ?? null,
    };
    if (
      !token
      || (previous.ownerId !== null && previous.ownerId !== next.ownerId)
      || (previous.projectId !== null && previous.projectId !== next.projectId)
    ) {
      clearCompletedQueryResults();
    }
    queryResultScopeRef.current = next;
  }, [currentProject?.id, token, user?.id]);

  useEffect(() => {
    if (!hydrated) return;
    const ownerId = token ? user?.id ?? '' : '';
    const previousOwnerId = profileOnboardingOwnerRef.current;
    if (previousOwnerId !== null && previousOwnerId !== ownerId) {
      setProfileOnboarding(null);
      clearPendingProfileOnboarding();
    }
    profileOnboardingOwnerRef.current = ownerId;
    if (!ownerId) {
      // eslint-disable-next-line react-hooks/set-state-in-effect -- auth loss must clear a pending notice immediately
      setProfileOnboarding(null);
      clearPendingProfileOnboarding();
      return;
    }
    const restored = readPendingProfileOnboarding(ownerId, sessionEpoch);
    setProfileOnboarding((current) => {
      if (current?.ownerId === ownerId) return { ...current, sessionEpoch };
      return restored;
    });
  }, [hydrated, sessionEpoch, token, user?.id]);

  const refreshNavCounts = useCallback(async () => {
    if (!token || !currentProject) return;
    const requestIdentity = requestIdentityRef.current;
    const projectId = currentProject.id;
    try {
      const status = await getStatus(projectId);
      if (
        requestIdentityRef.current !== requestIdentity
        || requestIdentity.projectId !== projectId
        || requestIdentity.token !== token
        || requestIdentity.sessionEpoch !== sessionEpoch
      ) return;
      setNavCounts((current) => ({
        sources: status.sourcesCount,
        concepts: status.conceptsCount,
        documentImportFailures: current.documentImportFailures,
      }));
    } catch {
      // Keep previous badge values on refresh failure (LWC-129).
    }
    try {
      const summary = await getDocumentImportFailureSummary(projectId);
      if (
        requestIdentityRef.current !== requestIdentity || requestIdentity.projectId !== projectId
        || requestIdentity.token !== token || requestIdentity.sessionEpoch !== sessionEpoch
      ) return;
      setNavCounts((current) => ({ ...current, documentImportFailures: summary.failed_count }));
    } catch {
      // Preserve the last known count when the failure summary is unavailable.
    }
  }, [token, currentProject, sessionEpoch]);

  const loadProjects = useCallback(async () => {
    const operationToken = token;
    if (!operationToken) return;
    const operationSessionEpoch = sessionEpoch;
    const startedGeneration = ++projectListGenerationRef.current;
    activeProjectLoadRef.current = {
      token: operationToken,
      sessionEpoch: operationSessionEpoch,
      generation: startedGeneration,
    };
    const operationOwnerId = user?.id ?? operationToken;
    const ownsState = () => (
      projectStateActiveRef.current
      && projectListGenerationRef.current === startedGeneration
      && requestIdentityRef.current.token === operationToken
      && requestIdentityRef.current.sessionEpoch === operationSessionEpoch
    );
    setProjectsLoading(true);
    setProjectsError('');
    try {
      if (setupReadyOwnerRef.current !== operationOwnerId) {
        setProductSetupPending(true);
        let bootstrap = bootstrapFlightsRef.current.get(operationToken);
        if (!bootstrap) {
          bootstrap = bootstrapDefaultProject(operationToken);
          bootstrapFlightsRef.current.set(operationToken, bootstrap);
        }
        try {
          await bootstrap;
        } finally {
          if (bootstrapFlightsRef.current.get(operationToken) === bootstrap) {
            bootstrapFlightsRef.current.delete(operationToken);
          }
        }
        if (!ownsState()) return;
        setupReadyOwnerRef.current = operationOwnerId;
        setProductSetupPending(false);
      }

      let nextProjects = await getProjects();
      if (!ownsState()) return;
      if (nextProjects.length === 0) {
        setProductSetupPending(true);
        await bootstrapDefaultProject(operationToken);
        if (!ownsState()) return;
        setupReadyOwnerRef.current = operationOwnerId;
        nextProjects = await getProjects();
        if (!ownsState()) return;
      }
      if (nextProjects.length === 0) {
        throw new Error('Workspace setup is pending; try again.');
      }
      const selected = selectDefaultProject(nextProjects, readLastProjectId());
      setProjects(nextProjects);
      setCurrentProject(selected);
      writeLastProjectId(selected?.id ?? null);
      setProductSetupPending(false);
    } catch (error) {
      if (!ownsState()) return;
      setupReadyOwnerRef.current = null;
      setProjects([]);
      setCurrentProject(null);
      setProductSetupPending(true);
      setProjectsError(error instanceof Error ? error.message : 'Unable to load projects.');
    } finally {
      if (ownsState()) setProjectsLoading(false);
      if (activeProjectLoadRef.current?.generation === startedGeneration) {
        activeProjectLoadRef.current = null;
      }
    }
  }, [sessionEpoch, token, user?.id]);

  const signIn = useCallback(async (email: string, password: string) => {
    await login(email, password);
    consumeExpiredLoginRedirect(router);
  }, [login, router]);

  const signInAsDemo = useCallback(async () => {
    await loginAsDemo();
    consumeExpiredLoginRedirect(router);
  }, [loginAsDemo, router]);

  const signOut = useCallback(async () => {
    if (!confirmNavigation()) return;
    await logout();
    try {
      clearForceHomeRedirect(typeof window !== 'undefined' ? window.localStorage : null);
    } catch {
      // Storage cleanup must not interrupt a completed sign out.
    }
    writeLastProjectId(null);
    clearPendingProfileOnboarding();
    setProjects([]);
    setCurrentProject(null);
    setProjectsLoading(false);
    setProjectsError('');
    setProductSetupPending(false);
    setupReadyOwnerRef.current = null;
    bootstrapFlightsRef.current.clear();
    setNewProjectOpen(false);
    setProfileOnboarding(null);
    setNavCounts(emptyNavCounts);
  }, [confirmNavigation, logout]);

  const selectProject = useCallback((projectId: string, destination?: string) => {
    const selected = projects.find((project) => project.id === projectId);
    if (!selected) return;
    // Same project: keep current route/query as-is.
    if (selected.id === currentProject?.id) return;
    if (!confirmNavigation()) return;
    writeLastProjectId(selected.id);
    setCurrentProject(selected);
    setNavCounts(emptyNavCounts);
    // Concept/source/detail routes and home search query belong to the previous
    // project — always land on a clean home after switching.
    router.replace(destination ?? '/');
  }, [confirmNavigation, currentProject?.id, projects, router]);

  const addProject = useCallback(async (name: string, description = '', idempotencyKey?: string) => {
    if (!token) throw new Error('Please log in to create a project.');
    if (isDemoSession) throw new Error('Demo mode cannot create projects.');
    const startedListGeneration = projectListGenerationRef.current;
    const operationEpoch = sessionEpoch;
    const operationToken = token;
    const operationOwnerId = user?.id ?? '';
    const project = await createProject(name, description, idempotencyKey);
    if (
      !projectStateActiveRef.current
      || projectListGenerationRef.current !== startedListGeneration
      || sessionEpoch !== operationEpoch
      || token !== operationToken
      || (user?.id ?? '') !== operationOwnerId
    ) return project;
    projectListGenerationRef.current += 1;
    const intent = user?.id ? { ownerId: user.id, projectId: project.id, sessionEpoch } : null;
    if (intent) {
      setProfileOnboarding(intent);
      writePendingProfileOnboarding(intent);
    }
    setProjectsLoading(false);
    setProjects((current) => {
      const withoutDuplicate = current.filter((item) => item.id !== project.id);
      return [...withoutDuplicate, project];
    });
    writeLastProjectId(project.id);
    setCurrentProject(project);
    setProjectsError('');
    return project;
  }, [isDemoSession, sessionEpoch, token, user]);

  const updateProjectDescription = useCallback((projectId: string, description: string, revision: number) => {
    setProjects((currentProjects) => currentProjects.map((project) => (
      project.id === projectId ? { ...project, description, descriptionRevision: revision } : project
    )));
    setCurrentProject((current) => (
      current?.id === projectId ? { ...current, description, descriptionRevision: revision } : current
    ));
  }, []);

  const markProfileOnboardingPresented = useCallback((ownerId: string, projectId: string, expectedSessionEpoch: number) => {
    const intent = profileOnboarding;
    if (
      !token
      || isDemoSession
      || !user?.id
      || user.id !== ownerId
      || currentProject?.id !== projectId
      || sessionEpoch !== expectedSessionEpoch
      || intent?.ownerId !== ownerId
      || intent.projectId !== projectId
      || intent.sessionEpoch !== expectedSessionEpoch
    ) return false;

    if (profileOnboardingWasPresented(ownerId, projectId)) {
      setProfileOnboarding(null);
      clearPendingProfileOnboarding();
      return false;
    }

    const key = profileOnboardingPresentedKey(ownerId, projectId);
    profileOnboardingPresentedMemory.add(key);
    try {
      window.localStorage.setItem(key, '1');
    } catch {
      // The module memory prevents repeat prompts for this tab lifetime.
    }
    setProfileOnboarding(null);
    clearPendingProfileOnboarding();
    return true;
  }, [currentProject?.id, isDemoSession, profileOnboarding, sessionEpoch, token, user]);

  const renameProject = useCallback(async (projectId: string, name: string) => {
    if (!token) throw new Error('Please log in to rename projects.');
    const selected = projects.find((project) => project.id === projectId);
    if (!selected) throw new Error('Please select a project before renaming.');
    const startedListGeneration = projectListGenerationRef.current;
    const startedRenameGeneration = (renameGenerationByProjectRef.current.get(projectId) ?? 0) + 1;
    const operationEpoch = sessionEpoch;
    const operationToken = token;
    renameGenerationByProjectRef.current.set(projectId, startedRenameGeneration);
    const nextName = await renameProjectRequest(projectId, name);
    if (
      !projectStateActiveRef.current
      || projectListGenerationRef.current !== startedListGeneration
      || renameGenerationByProjectRef.current.get(projectId) !== startedRenameGeneration
      || sessionEpoch !== operationEpoch
      || token !== operationToken
    ) return nextName;
    setProjects((currentProjects) => (
      currentProjects.map((project) => (
        project.id === projectId ? { ...project, name: nextName } : project
      ))
    ));
    setCurrentProject((current) => (
      current?.id === projectId ? { ...current, name: nextName } : current
    ));
    return nextName;
  }, [projects, sessionEpoch, token]);

  const refreshProjects = useCallback(async () => {
    if (token) await loadProjects();
  }, [loadProjects, token]);

  useEffect(() => {
    if (!hydrated) return;
    const ownerId = token ? user?.id ?? null : null;
    const previous = autoLoadIdentityRef.current;
    const shouldLoad = (
      previous === null
      || previous.token !== token
      || previous.ownerId !== ownerId
      || restartProjectLoadRef.current
    );
    autoLoadIdentityRef.current = { token, ownerId, sessionEpoch };
    if (token) {
      if (!shouldLoad) return;
      restartProjectLoadRef.current = false;
      void loadProjects();
      return;
    }

    restartProjectLoadRef.current = false;
    projectListGenerationRef.current += 1;
    // eslint-disable-next-line react-hooks/set-state-in-effect -- clear project state immediately after auth loss invalidates pending loads
    setProjectsLoading(false);
    setProjects([]);
    setCurrentProject(null);
    setProjectsError('');
    setupReadyOwnerRef.current = null;
    bootstrapFlightsRef.current.clear();
    setProductSetupPending(false);
    setNavCounts(emptyNavCounts);
  }, [hydrated, loadProjects, sessionEpoch, token, user?.id]);

  // Load / reload sidebar counts when auth or project changes (LWC-129).
  useEffect(() => {
    if (!token || !currentProject) {
      return;
    }

    let ignore = false;
    const requestIdentity = requestIdentityRef.current;
    void getStatus(currentProject.id)
      .then((status) => {
        if (ignore || requestIdentityRef.current !== requestIdentity) return;
        setNavCounts((current) => ({
          sources: status.sourcesCount,
          concepts: status.conceptsCount,
          documentImportFailures: current.documentImportFailures,
        }));
      })
      .catch(() => {
        // Leave previous values; display layer derives null when no project.
      });
    void getDocumentImportFailureSummary(currentProject.id)
      .then((summary) => {
        if (ignore || requestIdentityRef.current !== requestIdentity) return;
        setNavCounts((current) => ({ ...current, documentImportFailures: summary.failed_count }));
      })
      .catch(() => {
        // Keep the last known value; an unavailable count is never presented as zero.
      });

    return () => {
      ignore = true;
    };
  }, [token, currentProject, sessionEpoch]);

  // Derive displayed counts so logout / no-project never shows stale badges
  // without a synchronous setState in the load effect (LWC-133-friendly).
  const displayedNavCounts =
    token && currentProject ? navCounts : emptyNavCounts;

  const value = useMemo<WorkspaceContextValue>(() => ({
    hydrated,
    token,
    user,
    sessionEpoch,
    projects,
    currentProject,
    projectsLoading,
    projectsError,
    productSetupPending,
    isDemoSession,
    loginOpen: hydrated && !token,
    newProjectOpen,
    profileOnboarding,
    markProfileOnboardingPresented,
    navCounts: displayedNavCounts,
    refreshNavCounts,
    signIn,
    signInAsDemo,
    register,
    signOut,
    selectProject,
    addProject,
    renameProject,
    updateProjectDescription,
    refreshProjects,
    openNewProject: () => {
      if (isDemoSession) return;
      setNewProjectOpen(true);
    },
    closeNewProject: () => setNewProjectOpen(false),
  }), [
    addProject,
    renameProject,
    updateProjectDescription,
    currentProject,
    displayedNavCounts,
    hydrated,
    isDemoSession,
    newProjectOpen,
    profileOnboarding,
    markProfileOnboardingPresented,
    projects,
    projectsError,
    productSetupPending,
    refreshNavCounts,
    register,
    signIn,
    signInAsDemo,
    projectsLoading,
    refreshProjects,
    selectProject,
    sessionEpoch,
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
