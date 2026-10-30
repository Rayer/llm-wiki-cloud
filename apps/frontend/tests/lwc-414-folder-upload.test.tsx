import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act } from 'react';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { RAW_UPLOAD_MAX_BYTES, type RawUploadResult } from '@/lib/api';
import { RawUploadPanel } from '@/components/RawUploadPanel';

if (!(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT) {
  (globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
}

type Deferred<T> = {
  promise: Promise<T>;
  resolve: (value: T) => void;
  reject: (reason?: unknown) => void;
};

type UploadCall = {
  file: File;
  projectId: string;
  complete: Deferred<RawUploadResult>;
};

const mocks = vi.hoisted(() => ({
  currentProject: { id: 'project-a', name: 'Project A' },
  isDemoSession: false,
  refreshNavCounts: vi.fn(async () => undefined),
  uploadRawFile: vi.fn(),
  translate: (key: string, params?: Record<string, string | number>) => key === 'RawUpload.retryFile'
    ? `Retry ${params?.filename ?? ''}`
    : key === 'RawUpload.fileProgress'
      ? `${params?.filename ?? ''}: ${params?.status ?? ''} ${params?.progress ?? 0}%`
    : ({
      'RawUpload.status.queued': 'Queued',
      'RawUpload.status.uploading': 'Uploading',
      'RawUpload.status.accepted': 'Accepted',
      'RawUpload.status.created': 'Created',
      'RawUpload.status.alreadyExists': 'Already exists',
      'RawUpload.status.replaced': 'Replaced',
      'RawUpload.status.failed': 'Failed',
      'RawUpload.retry': 'Retry',
    }[key] ?? key),
}));

vi.mock('@/lib/api', async () => ({
  ...await vi.importActual<typeof import('@/lib/api')>('@/lib/api'),
  uploadRawFile: mocks.uploadRawFile,
}));

vi.mock('@/lib/i18n', async () => {
  const actual = await vi.importActual<typeof import('@/lib/i18n')>('@/lib/i18n');
  const { i18nLaneTestTranslate } = await vi.importActual<typeof import('./i18n-lane-test-helper')>('./i18n-lane-test-helper');
  mocks.translate = (key, params) => i18nLaneTestTranslate(actual.translate, key, params);
  return { useT: () => ({ t: mocks.translate }) };
});
vi.mock('@/components/WorkspaceProvider', () => ({
  useWorkspace: () => ({
    currentProject: mocks.currentProject,
    isDemoSession: mocks.isDemoSession,
    refreshNavCounts: mocks.refreshNavCounts,
  }),
}));

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((innerResolve, innerReject) => {
    resolve = innerResolve;
    reject = innerReject;
  });
  return { promise, resolve, reject };
}

function resultFor(file: File): RawUploadResult {
  return {
    filename: file.name,
    path: `raw/${file.name}`,
    bytes: file.size,
    sha256: 'fixture-digest',
    status: 'created',
  };
}

function folderFile(path: string, name: string, contents = 'content'): File {
  const file = new File([contents], name);
  Object.defineProperty(file, 'webkitRelativePath', { value: path });
  return file;
}

function uploadInput() {
  const input = document.querySelector<HTMLInputElement>('#raw-file-upload');
  if (!input) throw new Error('Raw upload file input is not mounted');
  return input;
}

function folderInput() {
  const input = document.querySelector<HTMLInputElement>('#raw-file-upload-folder');
  if (!input) throw new Error('Folder upload file input is not mounted');
  return input;
}

function checkboxForPath(path: string) {
  const listItem = screen.getByText(path).closest('li');
  const checkbox = listItem?.querySelector<HTMLInputElement>('input[type="checkbox"]');
  if (!checkbox) throw new Error(`Folder candidate checkbox is not found for ${path}`);
  return checkbox;
}

const directoryPickerDescriptor = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'webkitdirectory');
let uploadCalls: UploadCall[] = [];

beforeEach(() => {
  Object.defineProperty(HTMLInputElement.prototype, 'webkitdirectory', {
    configurable: true,
    value: true,
  });
  mocks.currentProject = { id: 'project-a', name: 'Project A' };
  mocks.isDemoSession = false;
  uploadCalls = [];
  mocks.uploadRawFile.mockImplementation((file: File, _onProgress: (progress: number) => void, projectId: string) => {
    const complete = deferred<RawUploadResult>();
    uploadCalls.push({ file, projectId, complete });
    return complete.promise;
  });
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  if (directoryPickerDescriptor) {
    Object.defineProperty(HTMLInputElement.prototype, 'webkitdirectory', directoryPickerDescriptor);
  } else {
    delete (HTMLInputElement.prototype as { webkitdirectory?: boolean }).webkitdirectory;
  }
});

describe('LWC-414 folder import', () => {
  it('keeps selection local until confirmation and sends only the selected eligible files once', async () => {
    render(<RawUploadPanel title="Upload" hint="Upload files" />);
    const guide = folderFile('handbook/guide.md', 'guide.md', 'guide content');
    const duplicateA = folderFile('handbook/one/readme.md', 'readme.md', 'readme one');
    const duplicateB = folderFile('handbook/two/readme.md', 'readme.md', 'readme two');
    const unsupported = folderFile('handbook/image.exe', 'image.exe', 'binary');
    const empty = folderFile('handbook/empty.txt', 'empty.txt', '');
    const oversized = new File([new Uint8Array(RAW_UPLOAD_MAX_BYTES + 1)], 'large.md');
    const longFilename = new File(['content'], `${'é'.repeat(255)}.md`);
    const backslashName = new File(['content'], 'bad\\name.md');
    const driveName = new File(['content'], 'a:note.md');
    Object.defineProperty(oversized, 'webkitRelativePath', { value: 'handbook/large.md' });
    Object.defineProperty(longFilename, 'webkitRelativePath', { value: `handbook/${longFilename.name}` });
    Object.defineProperty(backslashName, 'webkitRelativePath', { value: `handbook/${backslashName.name}` });
    Object.defineProperty(driveName, 'webkitRelativePath', { value: `handbook/${driveName.name}` });

    await act(async () => {
      fireEvent.change(folderInput(), {
        target: { files: [guide, duplicateA, duplicateB, unsupported, empty, oversized, longFilename, backslashName, driveName] },
      });
    });

    expect(await screen.findByText('handbook/guide.md')).toBeDefined();
    expect(screen.getByText('handbook/one/readme.md')).toBeDefined();
    expect(screen.getAllByText('RawUpload.unsupportedFile')).toHaveLength(3);
    expect(screen.getByText('RawUpload.emptyFile')).toBeDefined();
    expect(screen.getByText('RawUpload.fileTooLarge')).toBeDefined();
    expect(screen.getByText('RawUpload.filenameTooLong')).toBeDefined();
    expect(screen.getAllByText((_, node) => (
      node?.tagName === 'P' && node.textContent?.includes('RawUpload.destination') === true
    ))).toHaveLength(9);
    expect(uploadCalls).toHaveLength(0);

    const confirm = screen.getByRole('button', { name: 'RawUpload.confirmImport' });
    expect((confirm as HTMLButtonElement).disabled).toBe(true);

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'RawUpload.clearSelection' }));
    });
    expect((confirm as HTMLButtonElement).disabled).toBe(true);

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'RawUpload.selectAllEligible' }));
    });
    expect((confirm as HTMLButtonElement).disabled).toBe(true);

    await act(async () => {
      fireEvent.click(checkboxForPath('handbook/two/readme.md'));
      fireEvent.click(checkboxForPath('handbook/guide.md'));
    });
    expect((confirm as HTMLButtonElement).disabled).toBe(false);

    await act(async () => {
      fireEvent.click(confirm);
    });
    await waitFor(() => expect(uploadCalls).toHaveLength(1));
    expect(uploadCalls.map((call) => call.file)).toEqual([duplicateA]);
    expect(uploadCalls[0].projectId).toBe('project-a');
    expect(uploadCalls[0].file.name).toBe('readme.md');

    await act(async () => uploadCalls[0].complete.resolve(resultFor(uploadCalls[0].file)));
  });

  it('clears candidates on cancel and Project switch, and leaves an empty folder idle', async () => {
    const view = render(<RawUploadPanel title="Upload" hint="Upload files" />);
    await act(async () => {
      fireEvent.change(folderInput(), { target: { files: [folderFile('docs/guide.md', 'guide.md')] } });
    });
    expect(await screen.findByText('docs/guide.md')).toBeDefined();

    await act(async () => fireEvent.click(screen.getByRole('button', { name: 'RawUpload.cancel' })));
    await waitFor(() => expect(screen.queryByText('docs/guide.md')).toBeNull());
    expect(uploadCalls).toHaveLength(0);

    await act(async () => {
      fireEvent.change(folderInput(), { target: { files: [folderFile('docs/guide.md', 'guide.md')] } });
    });
    expect(await screen.findByText('docs/guide.md')).toBeDefined();

    mocks.currentProject = { id: 'project-b', name: 'Project B' };
    view.rerender(<RawUploadPanel title="Upload" hint="Upload files" />);
    await waitFor(() => expect(screen.queryByText('docs/guide.md')).toBeNull());
    expect(uploadCalls).toHaveLength(0);

    await act(async () => {
      fireEvent.change(folderInput(), { target: { files: [] } });
    });
    expect(await screen.findByText('RawUpload.emptyFolder')).toBeDefined();
    expect(uploadCalls).toHaveLength(0);
  });

  it('pins queued uploads and retries to their original Project after a switch', async () => {
    const view = render(<RawUploadPanel title="Upload" hint="Upload files" />);
    const files = ['one.md', 'two.md', 'three.md', 'four.md'].map((name) => new File([name], name));
    await act(async () => {
      fireEvent.change(uploadInput(), { target: { files } });
    });
    await waitFor(() => expect(uploadCalls).toHaveLength(3));
    expect(uploadCalls.map((call) => call.projectId)).toEqual(['project-a', 'project-a', 'project-a']);

    mocks.currentProject = { id: 'project-b', name: 'Project B' };
    view.rerender(<RawUploadPanel title="Upload" hint="Upload files" />);

    await act(async () => uploadCalls[0].complete.resolve(resultFor(uploadCalls[0].file)));
    await waitFor(() => expect(uploadCalls).toHaveLength(4));
    expect(uploadCalls[3].file.name).toBe('four.md');
    expect(uploadCalls[3].projectId).toBe('project-a');

    await act(async () => uploadCalls[1].complete.reject(new Error('temporary network failure')));
    const retry = await screen.findByRole('button', { name: 'Retry two.md' });
    await act(async () => fireEvent.click(retry));
    await waitFor(() => expect(uploadCalls).toHaveLength(5));
    expect(uploadCalls[4].file.name).toBe('two.md');
    expect(uploadCalls[4].projectId).toBe('project-a');

    await act(async () => {
      for (const call of uploadCalls.slice(2)) call.complete.resolve(resultFor(call.file));
    });
  });

  it('allows a later batch to reuse a historical basename', async () => {
    mocks.uploadRawFile.mockImplementation(async (file: File, _onProgress: (progress: number) => void, projectId: string) => {
      uploadCalls.push({ file, projectId, complete: deferred<RawUploadResult>() });
      return resultFor(file);
    });
    render(<RawUploadPanel title="Upload" hint="Upload files" />);
    const file = new File(['same content'], 'reused.md');

    await act(async () => fireEvent.change(uploadInput(), { target: { files: [file] } }));
    await waitFor(() => expect(screen.getByText('Created')).toBeDefined());
    await act(async () => fireEvent.change(uploadInput(), { target: { files: [file] } }));
    await waitFor(() => expect(uploadCalls).toHaveLength(2));
    expect(uploadCalls.map((call) => call.file.name)).toEqual(['reused.md', 'reused.md']);
    expect(uploadCalls.map((call) => call.projectId)).toEqual(['project-a', 'project-a']);
  });

  it('keeps the ordinary multi-file picker available when folder selection is unsupported', async () => {
    delete (HTMLInputElement.prototype as { webkitdirectory?: boolean }).webkitdirectory;
    render(<RawUploadPanel title="Upload" hint="Upload files" />);

    expect(await screen.findByText('RawUpload.folderUnsupportedBrowser')).toBeDefined();
    expect((screen.getByRole('button', { name: 'RawUpload.chooseFolder' }) as HTMLButtonElement).disabled).toBe(true);
    expect(uploadInput().disabled).toBe(false);

    const file = new File(['fallback content'], 'fallback.md');
    await act(async () => fireEvent.change(uploadInput(), { target: { files: [file] } }));
    await waitFor(() => expect(uploadCalls).toHaveLength(1));
    expect(uploadCalls[0].projectId).toBe('project-a');
    await act(async () => uploadCalls[0].complete.resolve(resultFor(file)));
  });
});
