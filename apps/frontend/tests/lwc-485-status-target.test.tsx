import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { resolve } from 'node:path';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { readFileSync } from 'node:fs';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

const mocks = vi.hoisted(() => ({
  currentProject: { id: 'project-a', name: 'Workspace A' },
  projects: [
    { id: 'project-a', name: 'Workspace A' },
    { id: 'project-b', name: 'Project B' },
  ],
  projectsLoading: false,
  hydrated: true,
  token: 'synthetic-test-token',
  sessionIdentityEpoch: 1,
  isDemoSession: false,
  user: { id: 'owner-a' },
  getBuildInfo: vi.fn(),
  getPipelineStatus: vi.fn(),
  getStatus: vi.fn(),
}));

vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api');
  return {
    ...actual,
    getBuildInfo: mocks.getBuildInfo,
    getPipelineStatus: mocks.getPipelineStatus,
    getStatus: mocks.getStatus,
  };
});

vi.mock('@/lib/auth', () => ({
  useAuth: () => ({ user: mocks.user }),
}));

vi.mock('@/lib/i18n', async () => {
  const actual = await vi.importActual<typeof import('@/lib/i18n')>('@/lib/i18n');
  const { i18nLaneTestTranslate } = await vi.importActual<typeof import('./i18n-lane-test-helper')>('./i18n-lane-test-helper');
  return {
    useT: () => ({ t: (key: string) => i18nLaneTestTranslate(actual.translate, key) }),
  };
});

vi.mock('@/components/WorkspaceProvider', () => ({
  useWorkspace: () => ({
    currentProject: mocks.currentProject,
    projects: mocks.projects,
    projectsLoading: mocks.projectsLoading,
    hydrated: mocks.hydrated,
    token: mocks.token,
    sessionIdentityEpoch: mocks.sessionIdentityEpoch,
    isDemoSession: mocks.isDemoSession,
  }),
}));

import StatusPage from '../src/app/(workspace)/status/page';
import { StatusClient } from '@/components/StatusClient';

const selectedProjectStatus = {
  sourcesCount: 1,
  conceptsCount: 2,
  rawCount: 3,
  suggestedQueries: [],
  lastExecution: { name: 'workspace-a-latest', status: 'SUCCEEDED' },
  quota: null,
  scheduledPipeline: null,
  raw: {},
};

const nativeCloudRunFailureStatus = JSON.parse(readFileSync(
  resolve(process.cwd(), '../bff/internal/handler/v1/testdata/pipeline-status-cloud-run-failure.json'),
  'utf8',
));

function buildInfo() {
  return {
    environment: 'dev' as const,
    product_version: 'test',
    commit: 'test',
    branch: 'test',
    tag: '',
    image_tag: 'test',
    service: 'test',
    revision: 'test',
  };
}

beforeEach(() => {
  mocks.currentProject = { id: 'project-a', name: 'Workspace A' };
  mocks.projects = [
    { id: 'project-a', name: 'Workspace A' },
    { id: 'project-b', name: 'Project B' },
  ];
  mocks.projectsLoading = false;
  mocks.hydrated = true;
  mocks.token = 'synthetic-test-token';
  mocks.sessionIdentityEpoch = 1;
  mocks.isDemoSession = false;
  mocks.user = { id: 'owner-a' };
  mocks.getBuildInfo.mockResolvedValue(buildInfo());
  mocks.getPipelineStatus.mockReset();
  mocks.getStatus.mockResolvedValue(selectedProjectStatus);
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

describe('LWC-485 exact status route reader', () => {
  it('passes promised route parameters and reads the exact owned Project/run without changing Workspace A', async () => {
    const run = {
      name: 'older-failed-run',
      status: 'FAILED',
      publication_outcome: 'confirmed_not_published',
      failure_reason: 'The confirmed run did not publish.',
      diagnostic: {
        stage: 'synto_run',
        error_class: 'execution_failed',
        detail_code: 'synthetic_failure',
        message: 'The confirmed run did not publish.',
      },
    };
    mocks.getPipelineStatus.mockResolvedValue({ project_id: 'project-b', last_execution: run });

    const route = await StatusPage({
      searchParams: Promise.resolve({ project_id: 'project-b', execution_id: 'older-failed-run' }),
    });
    expect(route.props).toMatchObject({
      requestedProjectId: 'project-b',
      requestedExecutionId: 'older-failed-run',
    });

    render(route);
    const target = await screen.findByTestId('status-requested-pipeline-run');
    await waitFor(() => expect(target.textContent).toContain('The confirmed run did not publish.'));

    expect(target.textContent).toContain('Project B');
    expect(target.textContent).toContain('older-failed-run');
    expect(target.textContent).toContain('FAILED');
    expect(target.textContent).toContain('Confirmed not published');
    expect(mocks.getPipelineStatus).toHaveBeenCalledWith('project-b', 'older-failed-run');
    await waitFor(() => expect(mocks.getStatus).toHaveBeenCalledWith('project-a'));
    expect(mocks.currentProject.id).toBe('project-a');
  });

  it('reads the native BFF Cloud Run full resource name for the exact short route ID', async () => {
    // This fixture is checked against the native PipelineStatus handler serializer in handler_test.go.
    const response = structuredClone(nativeCloudRunFailureStatus);
    mocks.projects = [
      { id: 'project-a', name: 'Workspace A' },
      { id: response.project_id, name: 'Project B' },
    ];
    mocks.getPipelineStatus.mockResolvedValue(response);
    const route = await StatusPage({
      searchParams: Promise.resolve({ project_id: 'demo-project', execution_id: 'owned-failure' }),
    });
    expect(route.props).toMatchObject({
      requestedProjectId: 'demo-project',
      requestedExecutionId: 'owned-failure',
    });
    const { unmount } = render(route);

    const target = await screen.findByTestId('status-requested-pipeline-run');
    await waitFor(() => expect(target.textContent).toContain('A readable Cloud Run failure'));
    expect(response.last_execution.name).toBe(
      'projects/llm-wiki-cloud/locations/asia-east1/jobs/olw-pipeline/executions/owned-failure',
    );
    expect(target.textContent).toContain('Project B');
    expect(target.textContent).toContain('owned-failure');
    expect(target.textContent).toContain('FAILED');
    expect(mocks.getPipelineStatus).toHaveBeenCalledWith('demo-project', 'owned-failure');
    await waitFor(() => expect(mocks.getStatus).toHaveBeenCalledWith('project-a'));
    expect(mocks.currentProject.id).toBe('project-a');
    unmount();
  });

  it.each([
    ['missing execution', 'project-b', undefined],
    ['duplicate project parameter', ['project-b', 'project-a'], 'older-failed-run'],
    ['unsafe execution segment', 'project-b', '../other-run'],
  ])('fails closed for %s', async (_label, requestedProjectId, requestedExecutionId) => {
    const { unmount } = render(
      <StatusClient
        requestedProjectId={requestedProjectId}
        requestedExecutionId={requestedExecutionId}
      />,
    );

    await screen.findByTestId('status-requested-pipeline-run');
    await waitFor(() => expect(screen.getByTestId('status-requested-pipeline-run').textContent).toContain('No pipeline status available.'));
    expect(mocks.getPipelineStatus).not.toHaveBeenCalled();
    expect(mocks.currentProject.id).toBe('project-a');
    unmount();
  });

  it.each([
    ['different short ID', 'a-different-execution'],
    ['different Cloud Run resource ID', 'projects/llm-wiki-cloud/locations/asia-east1/jobs/olw-pipeline/executions/a-different-execution'],
    ['Cloud Run child resource suffix', 'projects/llm-wiki-cloud/locations/asia-east1/jobs/olw-pipeline/executions/older-failed-run/child'],
    ['non-Cloud Run resource prefix', 'other/projects/llm-wiki-cloud/locations/asia-east1/jobs/olw-pipeline/executions/older-failed-run'],
  ])('hides a %s and does not disclose its details', async (_label, executionName) => {
    mocks.getPipelineStatus.mockResolvedValue({
      project_id: 'project-b',
      last_execution: { name: executionName, status: 'FAILED', failure_reason: 'must not appear' },
    });
    render(<StatusClient requestedProjectId="project-b" requestedExecutionId="older-failed-run" />);

    const target = await screen.findByTestId('status-requested-pipeline-run');
    await waitFor(() => expect(target.textContent).toContain('No pipeline status available.'));
    expect(target.textContent).not.toContain(executionName);
    expect(target.textContent).not.toContain('must not appear');
    expect(mocks.getPipelineStatus).toHaveBeenCalledWith('project-b', 'older-failed-run');
    expect(mocks.currentProject.id).toBe('project-a');
  });

  it('fails closed when the exact run response belongs to a different Project', async () => {
    mocks.getPipelineStatus.mockResolvedValue({
      project_id: 'project-a',
      last_execution: {
        name: 'projects/llm-wiki-cloud/locations/asia-east1/jobs/olw-pipeline/executions/older-failed-run',
        status: 'FAILED',
        failure_reason: 'another Project failure must not appear',
      },
    });
    render(<StatusClient requestedProjectId="project-b" requestedExecutionId="older-failed-run" />);

    const target = await screen.findByTestId('status-requested-pipeline-run');
    await waitFor(() => expect(target.textContent).toContain('No pipeline status available.'));
    expect(target.textContent).not.toContain('Project B');
    expect(target.textContent).not.toContain('another Project failure');
    expect(mocks.currentProject.id).toBe('project-a');
  });

  it('fails closed when the exact run is no longer visible to this owner', async () => {
    mocks.getPipelineStatus.mockRejectedValue(new Error('404 private Project/run unavailable'));
    render(<StatusClient requestedProjectId="project-b" requestedExecutionId="older-failed-run" />);

    const target = await screen.findByTestId('status-requested-pipeline-run');
    await waitFor(() => expect(target.textContent).toContain('No pipeline status available.'));
    expect(target.textContent).not.toContain('private Project/run unavailable');
    expect(target.textContent).not.toContain('Project B');
    expect(mocks.currentProject.id).toBe('project-a');
  });

  it('does not read a Project target absent from the authenticated Project list', async () => {
    mocks.projects = [{ id: 'project-a', name: 'Workspace A' }];
    render(<StatusClient requestedProjectId="not-owned" requestedExecutionId="older-failed-run" />);

    const target = await screen.findByTestId('status-requested-pipeline-run');
    await waitFor(() => expect(target.textContent).toContain('No pipeline status available.'));
    expect(mocks.getPipelineStatus).not.toHaveBeenCalled();
    expect(target.textContent).not.toContain('Project B');
    expect(mocks.currentProject.id).toBe('project-a');
  });

  it('keeps the ordinary status route on the globally selected Project', async () => {
    const route = await StatusPage({ searchParams: Promise.resolve({}) });
    expect(route.props).toMatchObject({ requestedProjectId: undefined, requestedExecutionId: undefined });
    render(route);
    await screen.findByText('3');

    expect(mocks.getStatus).toHaveBeenCalledWith('project-a');
    expect(mocks.getPipelineStatus).not.toHaveBeenCalled();
    expect(screen.queryByTestId('status-requested-pipeline-run')).toBeNull();
  });

  it('does not let an older session request overwrite a newer session result', async () => {
    let resolveOld!: (value: unknown) => void;
    const oldSessionResponse = new Promise((resolve) => { resolveOld = resolve; });
    mocks.getPipelineStatus.mockReturnValueOnce(oldSessionResponse).mockResolvedValueOnce({
      project_id: 'project-b',
      last_execution: { name: 'older-failed-run', status: 'FAILED', failure_reason: 'current session detail' },
    });
    const view = render(<StatusClient requestedProjectId="project-b" requestedExecutionId="older-failed-run" />);
    await waitFor(() => expect(mocks.getPipelineStatus).toHaveBeenCalledTimes(1));

    mocks.user = { id: 'owner-b' };
    mocks.sessionIdentityEpoch = 2;
    view.rerender(<StatusClient requestedProjectId="project-b" requestedExecutionId="older-failed-run" />);
    await waitFor(() => expect(mocks.getPipelineStatus).toHaveBeenCalledTimes(2));
    const target = await screen.findByTestId('status-requested-pipeline-run');
    await waitFor(() => expect(target.textContent).toContain('current session detail'));

    resolveOld({
      project_id: 'project-b',
      last_execution: { name: 'older-failed-run', status: 'FAILED', failure_reason: 'stale previous-session detail' },
    });
    await waitFor(() => expect(target.textContent).toContain('current session detail'));
    expect(target.textContent).not.toContain('stale previous-session detail');
  });
});
