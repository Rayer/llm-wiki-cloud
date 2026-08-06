import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act } from 'react';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';

if (!(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT) {
  (globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
}

type RawUploadResult = {
  filename: string;
  path: string;
  bytes: number;
  sha256: string;
  status: 'created' | 'already_exists';
};

type Deferred<T> = {
  promise: Promise<T>;
  resolve: (value: T) => void;
  reject: (reason?: unknown) => void;
};

type UploadCall = {
  fileName: string;
  onProgress: (progress: number) => void;
  complete: Deferred<RawUploadResult>;
};

const mocks = vi.hoisted(() => ({
  currentProject: { id: 'project-a', name: 'Project A' },
  getPipelineStatus: vi.fn(),
  refreshNavCounts: vi.fn(async () => undefined),
  uploadRawFile: vi.fn(),
}));

vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api');
  return {
    ...actual,
    getPipelineStatus: mocks.getPipelineStatus,
    uploadRawFile: mocks.uploadRawFile,
  };
});

vi.mock('@/lib/i18n', () => ({
  useT: () => ({ t: (key: string) => key }),
}));

vi.mock('@/components/WorkspaceProvider', () => ({
  useWorkspace: () => ({
    currentProject: mocks.currentProject,
    isDemoSession: false,
    refreshNavCounts: mocks.refreshNavCounts,
  }),
}));

import { PipelineClient } from '@/components/PipelineClient';

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((innerResolve, innerReject) => {
    resolve = innerResolve;
    reject = innerReject;
  });
  return { promise, resolve, reject };
}

let uploadCalls: UploadCall[] = [];

function getProgressBar(name: string) {
  return screen.getByRole('progressbar', { name: new RegExp(`${name}.*(Queued|Uploading|Failed|Created|Already exists)`) });
}

function getPercentText(name: string, percent: number) {
  return screen.getAllByText((_, node) => (
    node?.tagName === 'P' && node.textContent?.replace(/\s/g, '').includes(`${percent}%`)
  )).find((node) => {
    const row = node.closest('li');
    return row?.textContent?.includes(name) ?? false;
  }) ?? null;
}

function progressInput() {
  const input = document.querySelector<HTMLInputElement>('#raw-file-upload');
  if (!input) throw new Error('raw file input is not found');
  return input;
}

function resultFor(status: 'created' | 'already_exists'): RawUploadResult {
  return {
    filename: 'raw',
    path: 'raw',
    bytes: 8,
    sha256: 'sha256',
    status,
  };
}

beforeEach(() => {
  mocks.getPipelineStatus.mockResolvedValue({});
  uploadCalls = [];
  mocks.uploadRawFile.mockImplementation((file: File, onProgress: (progress: number) => void) => {
    const complete = deferred<RawUploadResult>();
    uploadCalls.push({
      fileName: file.name,
      onProgress,
      complete,
    });
    return complete.promise;
  });
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe('LWC-125 raw upload progress behavior', () => {
  it('holds the fourth file queued until one of three active uploads completes', async () => {
    render(<PipelineClient />);

    await act(async () => {
      fireEvent.change(progressInput(), {
        target: {
          files: [
            new File(['alpha'], 'alpha.md'),
            new File(['beta'], 'beta.md'),
            new File(['gamma'], 'gamma.md'),
            new File(['delta'], 'delta.md'),
          ],
        },
      } as unknown as React.ChangeEvent<HTMLInputElement>);
    });

    expect(uploadCalls).toHaveLength(3);
    expect(uploadCalls.map((call) => call.fileName)).toEqual(['alpha.md', 'beta.md', 'gamma.md']);
    expect(getProgressBar('delta.md').getAttribute('aria-label')).toContain('delta.md: Queued 0%');
    expect(getProgressBar('delta.md').getAttribute('aria-valuenow')).toBe('0');

    await act(async () => {
      uploadCalls[0].complete.resolve(resultFor('created'));
    });
    await waitFor(() => expect(uploadCalls).toHaveLength(4));
    expect(uploadCalls[3].fileName).toBe('delta.md');
    expect(getProgressBar('delta.md').getAttribute('aria-label')).toContain('delta.md: Uploading 0%');
    expect(getProgressBar('delta.md').getAttribute('aria-valuenow')).toBe('0');

    await act(async () => {
      uploadCalls[1].complete.resolve(resultFor('created'));
      uploadCalls[2].complete.resolve(resultFor('created'));
      uploadCalls[3].complete.resolve(resultFor('created'));
    });
    await waitFor(() => expect(getProgressBar('delta.md').getAttribute('aria-valuenow')).toBe('100'));
  });

  it('updates progress independently and retries failed uploads from zero', async () => {
    render(<PipelineClient />);

    await act(async () => {
      fireEvent.change(progressInput(), {
        target: {
          files: [new File(['alpha'], 'alpha.md'), new File(['beta'], 'beta.md')],
        },
      } as unknown as React.ChangeEvent<HTMLInputElement>);
    });

    expect(uploadCalls).toHaveLength(2);

    await act(async () => {
      uploadCalls[0].onProgress(14);
      uploadCalls[1].onProgress(55);
    });

    expect(getProgressBar('alpha.md').getAttribute('aria-valuenow')).toBe('14');
    expect(getProgressBar('beta.md').getAttribute('aria-valuenow')).toBe('55');
    expect(getProgressBar('alpha.md').getAttribute('aria-label')).toContain('alpha.md: Uploading');
    expect(getProgressBar('beta.md').getAttribute('aria-label')).toContain('beta.md: Uploading');
    expect(getPercentText('alpha.md', 14)?.textContent).toContain('14%');
    expect(getPercentText('beta.md', 55)?.textContent).toContain('55%');

    await act(async () => {
      uploadCalls[1].complete.resolve(resultFor('created'));
    });
    await waitFor(() => expect(getProgressBar('beta.md').getAttribute('aria-valuenow')).toBe('100'));
    expect(getProgressBar('alpha.md').getAttribute('aria-valuenow')).toBe('14');

    await act(async () => {
      uploadCalls[0].onProgress(67);
      uploadCalls[0].complete.reject(new Error('upload failed'));
    });
    await waitFor(() => expect(screen.getByRole('button', { name: 'Retry alpha.md' })).toBeDefined());
    expect(getProgressBar('alpha.md').getAttribute('aria-valuenow')).toBe('67');
    expect(getPercentText('alpha.md', 67)?.textContent).toContain('67%');
    expect(screen.getByText('Failed')).not.toBeNull();

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Retry alpha.md' }));
    });

    expect(uploadCalls).toHaveLength(3);
    await waitFor(() => {
      const progressBar = getProgressBar('alpha.md');
      expect(progressBar.getAttribute('aria-label')).toMatch(/alpha\.md: (Queued|Uploading) 0%/);
      expect(progressBar.getAttribute('aria-valuenow')).toBe('0');
    });

    await act(async () => {
      uploadCalls[2].onProgress(21);
      uploadCalls[2].complete.resolve(resultFor('already_exists'));
    });

    await waitFor(() => expect(getProgressBar('alpha.md').getAttribute('aria-valuenow')).toBe('100'));
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Retry alpha.md' })).toBeNull());
    expect(getProgressBar('beta.md').getAttribute('aria-valuenow')).toBe('100');
    expect(mocks.refreshNavCounts).toHaveBeenCalled();
  });
});
