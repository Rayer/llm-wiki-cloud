import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { StrictMode, useState } from 'react';

const mocks = vi.hoisted(() => ({
  getProjects: vi.fn(),
  createProject: vi.fn(),
  renameProject: vi.fn(),
  getStatus: vi.fn(),
  replace: vi.fn(),
  token: 'auth-token' as string | null,
}));

vi.mock('next/navigation', () => ({
  useRouter: () => ({
    replace: mocks.replace,
  }),
}));

vi.mock('@/lib/projects', async () => {
  const actual = await vi.importActual<typeof import('@/lib/projects')>('@/lib/projects');
  return {
    ...actual,
    getProjects: mocks.getProjects,
    createProject: mocks.createProject,
    renameProject: mocks.renameProject,
  };
});

vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api');
  return {
    ...actual,
    getStatus: mocks.getStatus,
  };
});

vi.mock('@/lib/auth', () => ({
  useAuth: () => ({
    accessToken: mocks.token,
    access_token: mocks.token,
    user: { id: 'user-1', email: 'owner@example.com', role: 'owner' },
    hydrated: true,
    isAuthenticated: true,
    isDemoSession: false,
    login: async () => undefined,
    loginAsDemo: async () => undefined,
    register: async () => undefined,
    logout: async () => undefined,
    refreshAccessToken: async () => 'auth-token',
  }),
}));

vi.mock('@/components/NavigationBlocker', () => ({
  useNavigationBlocker: () => ({
    confirmNavigation: () => true,
    setBlocked: () => undefined,
    consumeCapturedConfirmation: () => false,
  }),
}));

import { WorkspaceProvider, useWorkspace } from '@/components/WorkspaceProvider';

function ProjectStateProbe() {
  const {
    projects, currentProject, projectsLoading, projectsError,
    addProject, renameProject, refreshProjects,
  } = useWorkspace();
  const [result, setResult] = useState('');

  return (
    <div>
      {projects.map((project) => (
        <p key={project.id} data-testid={`project-name-${project.id}`}>{project.name}</p>
      ))}
      <p data-testid="current-project-id">{currentProject?.id ?? ''}</p>
      <p data-testid="current-project-name">{currentProject?.name ?? ''}</p>
      <p data-testid="result">{result}</p>
      <p data-testid="projects-loading">{String(projectsLoading)}</p>
      <p data-testid="projects-error">{projectsError}</p>
      <button type="button" onClick={async () => setResult((await addProject('Stale Add')).id)}>
        add project
      </button>
      <button
        type="button"
        onClick={async () => {
          try {
            const nextName = await renameProject('project-a', 'Rename from API');
            setResult(nextName);
          } catch (error) {
            setResult(error instanceof Error ? error.message : 'rename failed');
          }
        }}
      >
        rename success
      </button>
      <button
        type="button"
        onClick={async () => {
          try {
            await renameProject('project-a', 'Bad Name');
            setResult('no-error');
          } catch (error) {
            setResult(error instanceof Error ? error.message : 'rename failed');
          }
        }}
      >
        rename fail
      </button>
      <button type="button" onClick={() => void renameProject('project-a', 'First Rename')}>
        rename first
      </button>
      <button type="button" onClick={() => void renameProject('project-a', 'Second Rename')}>
        rename second
      </button>
      <button type="button" onClick={() => void renameProject('project-b', 'Other Rename')}>
        rename other
      </button>
      <button type="button" onClick={() => void refreshProjects()}>
        refresh projects
      </button>
    </div>
  );
}

function apiStatus() {
  return {
    sourcesCount: 0,
    conceptsCount: 0,
    rawCount: 0,
    suggestedQueries: [],
    lastExecution: null,
    raw: {},
  };
}

beforeEach(() => {
  mocks.token = 'auth-token';
  mocks.getProjects.mockResolvedValue([
    { id: 'project-a', name: 'Project Alpha' },
    { id: 'project-b', name: 'Project Beta' },
  ]);
  mocks.renameProject.mockResolvedValue('Rename from API');
  mocks.createProject.mockResolvedValue({ id: 'created', name: 'Created' });
  mocks.getStatus.mockResolvedValue(apiStatus());
  localStorage.setItem('llm-wiki-last-project', 'project-a');
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  localStorage.clear();
});

describe('LWC-174 WorkspaceProvider rename behavior', () => {
  it('updates project state after a successful rename under React StrictMode', async () => {
    render(
      <StrictMode>
        <WorkspaceProvider>
          <ProjectStateProbe />
        </WorkspaceProvider>
      </StrictMode>,
    );

    expect((await screen.findByTestId('project-name-project-a')).textContent).toBe('Project Alpha');
    fireEvent.click(screen.getByRole('button', { name: 'rename success' }));

    await waitFor(() => expect(screen.getByTestId('result').textContent).toBe('Rename from API'));
    expect(screen.getByTestId('project-name-project-a').textContent).toBe('Rename from API');
    expect(screen.getByTestId('current-project-name').textContent).toBe('Rename from API');
  });

  it('updates both project list and current project on successful rename', async () => {
    render(
      <WorkspaceProvider>
        <ProjectStateProbe />
      </WorkspaceProvider>,
    );

    expect((await screen.findByTestId('project-name-project-a')).textContent).toBe('Project Alpha');
    expect(screen.getByTestId('current-project-id').textContent).toBe('project-a');
    expect(screen.getByTestId('current-project-name').textContent).toBe('Project Alpha');

    fireEvent.click(screen.getByRole('button', { name: 'rename success' }));

    await waitFor(() => expect(screen.getByTestId('result').textContent).toBe('Rename from API'));
    expect(screen.getByTestId('project-name-project-a').textContent).toBe('Rename from API');
    expect(screen.getByTestId('project-name-project-b').textContent).toBe('Project Beta');
    expect(screen.getByTestId('current-project-id').textContent).toBe('project-a');
    expect(screen.getByTestId('current-project-name').textContent).toBe('Rename from API');
  });

  it('preserves old project names when rename fails', async () => {
    mocks.renameProject.mockRejectedValue(new Error('Name already exists.'));
    render(
      <WorkspaceProvider>
        <ProjectStateProbe />
      </WorkspaceProvider>,
    );

    expect((await screen.findByTestId('project-name-project-a')).textContent).toBe('Project Alpha');
    expect(screen.getByTestId('current-project-name').textContent).toBe('Project Alpha');

    fireEvent.click(screen.getByRole('button', { name: 'rename fail' }));
    await waitFor(() => expect(screen.getByTestId('result').textContent).toBe('Name already exists.'));
    expect(screen.getByTestId('project-name-project-a').textContent).toBe('Project Alpha');
    expect(screen.getByTestId('current-project-id').textContent).toBe('project-a');
    expect(screen.getByTestId('current-project-name').textContent).toBe('Project Alpha');
    expect(screen.getByTestId('project-name-project-b').textContent).toBe('Project Beta');
  });

  it('does not apply a stale rename completion after logout and same-token login to the same project id', async () => {
    let resolveRename!: (name: string) => void;
    mocks.renameProject.mockReturnValue(new Promise<string>((resolve) => {
      resolveRename = resolve;
    }));
    const view = () => (
      <WorkspaceProvider>
        <ProjectStateProbe />
      </WorkspaceProvider>
    );
    const rendered = render(view());
    expect((await screen.findByTestId('project-name-project-a')).textContent).toBe('Project Alpha');

    fireEvent.click(screen.getByRole('button', { name: 'rename success' }));
    mocks.token = null;
    rendered.rerender(view());
    await waitFor(() => expect(screen.queryByTestId('project-name-project-a')).toBeNull());

    mocks.getProjects.mockResolvedValue([{ id: 'project-a', name: 'Replacement Session Name' }]);
    mocks.token = 'auth-token';
    rendered.rerender(view());
    expect((await screen.findByTestId('project-name-project-a')).textContent).toBe('Replacement Session Name');

    resolveRename('Stale Rename');
    await waitFor(() => expect(screen.getByTestId('result').textContent).toBe('Stale Rename'));
    expect(screen.getByTestId('project-name-project-a').textContent).toBe('Replacement Session Name');
    expect(screen.getByTestId('current-project-name').textContent).toBe('Replacement Session Name');
  });

  it('does not publish a stale project creation after logout and same-token login', async () => {
    let resolveCreate!: (project: { id: string; name: string }) => void;
    mocks.createProject.mockReturnValue(new Promise((resolve) => { resolveCreate = resolve; }));
    const view = () => <WorkspaceProvider><ProjectStateProbe /></WorkspaceProvider>;
    const rendered = render(view());
    await screen.findByTestId('project-name-project-a');

    fireEvent.click(screen.getByRole('button', { name: 'add project' }));
    mocks.token = null;
    rendered.rerender(view());
    await waitFor(() => expect(screen.queryByTestId('project-name-project-a')).toBeNull());

    mocks.getProjects.mockResolvedValue([{ id: 'replacement', name: 'Replacement Session' }]);
    mocks.token = 'auth-token';
    rendered.rerender(view());
    await screen.findByTestId('project-name-replacement');

    resolveCreate({ id: 'stale-created', name: 'Stale Add' });
    await waitFor(() => expect(screen.getByTestId('result').textContent).toBe('stale-created'));
    expect(screen.queryByTestId('project-name-stale-created')).toBeNull();
    expect(screen.getByTestId('current-project-id').textContent).toBe('replacement');
    expect(localStorage.getItem('llm-wiki-last-project')).toBe('replacement');
  });

  it('clears loading immediately when logout invalidates a pending project load', async () => {
    let resolveRefresh!: (projects: { id: string; name: string }[]) => void;
    const view = () => <WorkspaceProvider><ProjectStateProbe /></WorkspaceProvider>;
    const rendered = render(view());
    await screen.findByTestId('project-name-project-a');
    mocks.getProjects.mockReturnValue(new Promise((resolve) => { resolveRefresh = resolve; }));

    fireEvent.click(screen.getByRole('button', { name: 'refresh projects' }));
    await waitFor(() => expect(screen.getByTestId('projects-loading').textContent).toBe('true'));
    mocks.token = null;
    rendered.rerender(view());

    await waitFor(() => expect(screen.getByTestId('projects-loading').textContent).toBe('false'));
    resolveRefresh([{ id: 'stale', name: 'Stale' }]);
    expect(screen.queryByTestId('project-name-stale')).toBeNull();
  });

  it('keeps the newer project load when two refreshes resolve out of order', async () => {
    let resolveFirst!: (projects: { id: string; name: string }[]) => void;
    let resolveSecond!: (projects: { id: string; name: string }[]) => void;
    render(<WorkspaceProvider><ProjectStateProbe /></WorkspaceProvider>);
    await screen.findByTestId('project-name-project-a');
    mocks.getProjects
      .mockReturnValueOnce(new Promise((resolve) => { resolveFirst = resolve; }))
      .mockReturnValueOnce(new Promise((resolve) => { resolveSecond = resolve; }));

    fireEvent.click(screen.getByRole('button', { name: 'refresh projects' }));
    fireEvent.click(screen.getByRole('button', { name: 'refresh projects' }));
    resolveSecond([{ id: 'newer', name: 'Newer Load' }]);
    await screen.findByTestId('project-name-newer');
    expect(screen.getByTestId('projects-loading').textContent).toBe('false');
    resolveFirst([{ id: 'older', name: 'Older Load' }]);
    await waitFor(() => expect(mocks.getProjects).toHaveBeenCalledTimes(3));
    expect(screen.queryByTestId('project-name-older')).toBeNull();
    expect(screen.getByTestId('project-name-newer').textContent).toBe('Newer Load');
    expect(screen.getByTestId('projects-loading').textContent).toBe('false');
  });

  it('lets a successful create invalidate an older project load', async () => {
    let resolveRefresh!: (projects: { id: string; name: string }[]) => void;
    mocks.getProjects.mockReturnValueOnce(new Promise((resolve) => { resolveRefresh = resolve; }));
    mocks.createProject.mockResolvedValue({ id: 'created', name: 'Created' });
    render(<WorkspaceProvider><ProjectStateProbe /></WorkspaceProvider>);

    await waitFor(() => expect(screen.getByTestId('projects-loading').textContent).toBe('true'));
    fireEvent.click(screen.getByRole('button', { name: 'add project' }));
    await waitFor(() => expect(screen.getByTestId('current-project-id').textContent).toBe('created'));
    resolveRefresh([{ id: 'older', name: 'Older Load' }]);

    await waitFor(() => expect(screen.getByTestId('project-name-created').textContent).toBe('Created'));
    expect(screen.getByTestId('projects-loading').textContent).toBe('false');
    expect(screen.queryByTestId('project-name-older')).toBeNull();
    expect(screen.getByTestId('current-project-id').textContent).toBe('created');
    expect(localStorage.getItem('llm-wiki-last-project')).toBe('created');
  });

  it('ignores an older project-load failure after a newer refresh succeeds', async () => {
    let rejectFirst!: (error: Error) => void;
    let resolveSecond!: (projects: { id: string; name: string }[]) => void;
    render(<WorkspaceProvider><ProjectStateProbe /></WorkspaceProvider>);
    await screen.findByTestId('project-name-project-a');
    mocks.getProjects
      .mockReturnValueOnce(new Promise((_, reject) => { rejectFirst = reject; }))
      .mockReturnValueOnce(new Promise((resolve) => { resolveSecond = resolve; }));

    fireEvent.click(screen.getByRole('button', { name: 'refresh projects' }));
    fireEvent.click(screen.getByRole('button', { name: 'refresh projects' }));
    resolveSecond([{ id: 'newer', name: 'Newer Load' }]);
    await screen.findByTestId('project-name-newer');
    rejectFirst(new Error('stale load failure'));
    await waitFor(() => expect(mocks.getProjects).toHaveBeenCalledTimes(3));
    expect(screen.getByTestId('projects-error').textContent).toBe('');
    expect(screen.getByTestId('project-name-newer').textContent).toBe('Newer Load');
    expect(screen.getByTestId('projects-loading').textContent).toBe('false');
  });

  it('does not let an older project load clear loading while a newer load is pending', async () => {
    let resolveFirst!: (projects: { id: string; name: string }[]) => void;
    let resolveSecond!: (projects: { id: string; name: string }[]) => void;
    render(<WorkspaceProvider><ProjectStateProbe /></WorkspaceProvider>);
    await screen.findByTestId('project-name-project-a');
    mocks.getProjects
      .mockReturnValueOnce(new Promise((resolve) => { resolveFirst = resolve; }))
      .mockReturnValueOnce(new Promise((resolve) => { resolveSecond = resolve; }));

    fireEvent.click(screen.getByRole('button', { name: 'refresh projects' }));
    fireEvent.click(screen.getByRole('button', { name: 'refresh projects' }));
    resolveFirst([{ id: 'older', name: 'Older Load' }]);
    await waitFor(() => expect(screen.getByTestId('projects-loading').textContent).toBe('true'));
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(screen.getByTestId('projects-loading').textContent).toBe('true');
    expect(screen.queryByTestId('project-name-older')).toBeNull();
    resolveSecond([{ id: 'newer', name: 'Newer Load' }]);
    await screen.findByTestId('project-name-newer');
    expect(screen.getByTestId('projects-loading').textContent).toBe('false');
  });

  it('keeps the newer rename when same-project requests resolve out of order', async () => {
    let resolveFirst!: (name: string) => void;
    let resolveSecond!: (name: string) => void;
    mocks.renameProject
      .mockReturnValueOnce(new Promise<string>((resolve) => { resolveFirst = resolve; }))
      .mockReturnValueOnce(new Promise<string>((resolve) => { resolveSecond = resolve; }));
    render(<WorkspaceProvider><ProjectStateProbe /></WorkspaceProvider>);
    await screen.findByTestId('project-name-project-a');

    fireEvent.click(screen.getByRole('button', { name: 'rename first' }));
    fireEvent.click(screen.getByRole('button', { name: 'rename second' }));
    resolveSecond('Second Rename');
    await waitFor(() => expect(screen.getByTestId('project-name-project-a').textContent).toBe('Second Rename'));
    resolveFirst('First Rename');
    await waitFor(() => expect(mocks.renameProject).toHaveBeenCalledTimes(2));
    expect(screen.getByTestId('project-name-project-a').textContent).toBe('Second Rename');
    expect(screen.getByTestId('current-project-name').textContent).toBe('Second Rename');
  });

  it('does not let an older rename overwrite a later authoritative project refresh', async () => {
    let resolveRename!: (name: string) => void;
    mocks.renameProject.mockReturnValue(new Promise<string>((resolve) => { resolveRename = resolve; }));
    render(<WorkspaceProvider><ProjectStateProbe /></WorkspaceProvider>);
    await screen.findByTestId('project-name-project-a');

    fireEvent.click(screen.getByRole('button', { name: 'rename first' }));
    mocks.getProjects.mockResolvedValue([
      { id: 'project-a', name: 'Authoritative Refresh' },
      { id: 'project-b', name: 'Project Beta' },
    ]);
    fireEvent.click(screen.getByRole('button', { name: 'refresh projects' }));
    await waitFor(() => expect(screen.getByTestId('project-name-project-a').textContent).toBe('Authoritative Refresh'));
    resolveRename('Stale Rename');
    await waitFor(() => expect(mocks.renameProject).toHaveBeenCalledTimes(1));
    expect(screen.getByTestId('project-name-project-a').textContent).toBe('Authoritative Refresh');
    expect(screen.getByTestId('current-project-name').textContent).toBe('Authoritative Refresh');
  });

  it('keeps valid concurrent renames for different projects', async () => {
    let resolveFirst!: (name: string) => void;
    let resolveOther!: (name: string) => void;
    mocks.renameProject
      .mockReturnValueOnce(new Promise<string>((resolve) => { resolveFirst = resolve; }))
      .mockReturnValueOnce(new Promise<string>((resolve) => { resolveOther = resolve; }));
    render(<WorkspaceProvider><ProjectStateProbe /></WorkspaceProvider>);
    await screen.findByTestId('project-name-project-a');

    fireEvent.click(screen.getByRole('button', { name: 'rename first' }));
    fireEvent.click(screen.getByRole('button', { name: 'rename other' }));
    resolveOther('Other Rename');
    resolveFirst('First Rename');
    await waitFor(() => expect(screen.getByTestId('project-name-project-a').textContent).toBe('First Rename'));
    expect(screen.getByTestId('project-name-project-b').textContent).toBe('Other Rename');
  });
});
