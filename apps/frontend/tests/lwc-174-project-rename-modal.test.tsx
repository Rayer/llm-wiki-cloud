import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

if (!(React as { act?: (callback: () => unknown) => Promise<unknown> | unknown }).act) {
  Object.defineProperty(React, 'act', {
    configurable: true,
    value: (callback: () => unknown) => Promise.resolve(callback()),
  });
}

const {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} = await import('@testing-library/react');

import { MAX_PROJECT_NAME_LENGTH } from '@/lib/projects';
import { ProjectRenameModal } from '@/components/ProjectRenameModal';

const project = { id: 'project-a', name: 'Project Alpha' };

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

beforeEach(() => {
  localStorage.setItem('locale', 'en');
});

describe('LWC-174 project rename modal behavior', () => {
  it('renames with a trimmed project name and closes on success', async () => {
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    const onClose = vi.fn();

    render(<ProjectRenameModal project={project} onSubmit={onSubmit} onClose={onClose} />);
    const input = screen.getByRole('textbox', { name: 'Project name' });

    fireEvent.change(input, { target: { value: '  Renamed Alpha  ' } });
    fireEvent.click(screen.getByRole('button', { name: 'Rename' }));

    await waitFor(() => expect(onSubmit).toHaveBeenCalledWith('Renamed Alpha'));
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
    expect(input.getAttribute('value')).toEqual('  Renamed Alpha  ');
  });

  it('preserves input and blocks duplicate submit while request is pending', async () => {
    const deferredRename = deferred<void>();
    const onSubmit = vi.fn().mockImplementation(() => deferredRename.promise);
    const onClose = vi.fn();

    render(<ProjectRenameModal project={project} onSubmit={onSubmit} onClose={onClose} />);
    const input = screen.getByRole('textbox', { name: 'Project name' });
    const submitButton = screen.getByRole('button', { name: 'Rename' }) as HTMLButtonElement;

    fireEvent.change(input, { target: { value: 'Renamed Alpha' } });
    fireEvent.click(submitButton);

    await waitFor(() => expect(submitButton.disabled).toBe(true));
    fireEvent.click(submitButton);

    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1));
    expect(submitButton.disabled).toBe(true);
    deferredRename.resolve();
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
    expect(input.getAttribute('value')).toEqual('Renamed Alpha');
  });

  it('renders inline validation errors and avoids server calls for invalid input', async () => {
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    const onClose = vi.fn();

    render(<ProjectRenameModal project={project} onSubmit={onSubmit} onClose={onClose} />);
    const input = screen.getByRole('textbox', { name: 'Project name' });

    fireEvent.change(input, { target: { value: '   ' } });
    fireEvent.click(screen.getByRole('button', { name: 'Rename' }));

    expect(screen.getByText('Project name is required.')).toBeDefined();
    expect(onSubmit).not.toHaveBeenCalled();
    expect(input.getAttribute('value')).toEqual('   ');
  });

  it('accepts exactly 64 emoji code points in project name validation', async () => {
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    const onClose = vi.fn();

    render(<ProjectRenameModal project={project} onSubmit={onSubmit} onClose={onClose} />);
    const maxEmojiName = '🧪'.repeat(MAX_PROJECT_NAME_LENGTH);

    const input = screen.getByRole('textbox', { name: 'Project name' });
    fireEvent.change(input, { target: { value: maxEmojiName } });

    expect(input.getAttribute('maxLength')).toBeNull();
    expect(screen.queryByText('Project name must be 1-64 characters.')).toBeNull();
    const submitButton = screen.getByRole('button', { name: 'Rename' }) as HTMLButtonElement;
    expect(submitButton.disabled).toBe(false);
    fireEvent.click(submitButton);

    await waitFor(() => expect(onSubmit).toHaveBeenCalledWith(maxEmojiName));
    expect(onSubmit).toHaveBeenCalledTimes(1);
    expect([...onSubmit.mock.calls[0][0]]).toHaveLength(MAX_PROJECT_NAME_LENGTH);
    expect(screen.queryByText('Project name must be 1-64 characters.')).toBeNull();
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('rejects 65 emoji code points with custom validation', () => {
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    const onClose = vi.fn();

    render(<ProjectRenameModal project={project} onSubmit={onSubmit} onClose={onClose} />);
    const longName = '🧪'.repeat(MAX_PROJECT_NAME_LENGTH + 1);

    fireEvent.change(screen.getByRole('textbox', { name: 'Project name' }), { target: { value: longName } });

    expect(screen.getByText('Project name must be 1-64 characters.')).toBeDefined();
    expect((screen.getByRole('button', { name: 'Rename' }) as HTMLButtonElement).disabled).toBe(true);
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it('keeps input and shows server error when rename API fails', async () => {
    const onSubmit = vi.fn().mockRejectedValue(new Error('Name already exists'));
    const onClose = vi.fn();

    render(<ProjectRenameModal project={project} onSubmit={onSubmit} onClose={onClose} />);
    const input = screen.getByRole('textbox', { name: 'Project name' });

    fireEvent.change(input, { target: { value: 'Project Beta' } });
    fireEvent.click(screen.getByRole('button', { name: 'Rename' }));

    expect(await screen.findByText('Name already exists')).toBeDefined();
    expect(onClose).not.toHaveBeenCalled();
    expect(input.getAttribute('value')).toEqual('Project Beta');
  });

  it('closes immediately on cancel without sending rename request', () => {
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    const onClose = vi.fn();

    render(<ProjectRenameModal project={project} onSubmit={onSubmit} onClose={onClose} />);
    fireEvent.change(screen.getByRole('textbox', { name: 'Project name' }), { target: { value: 'Project Beta' } });
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));

    expect(onSubmit).not.toHaveBeenCalled();
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('localizes rename labels and validation in Traditional Chinese', () => {
    localStorage.setItem('locale', 'zh-TW');
    render(<ProjectRenameModal project={project} onSubmit={vi.fn()} onClose={vi.fn()} />);

    expect(screen.getByRole('heading', { name: '重新命名專案' })).toBeDefined();
    const input = screen.getByRole('textbox', { name: '專案名稱' });
    fireEvent.change(input, { target: { value: '   ' } });
    expect(screen.getByText('請輸入專案名稱。')).toBeDefined();
    fireEvent.change(input, { target: { value: '專案名稱'.repeat(17) } });
    expect(screen.getByText('專案名稱需為 1-64 個字元。')).toBeDefined();
    expect(screen.getByRole('button', { name: '關閉重新命名專案對話框' })).toBeDefined();
    expect(screen.getByRole('button', { name: '取消' })).toBeDefined();
    expect(screen.getByRole('button', { name: '重新命名' })).toBeDefined();
  });

  it('localizes provisional project naming copy in Traditional Chinese', () => {
    localStorage.setItem('locale', 'zh-TW');
    render(<ProjectRenameModal project={{ id: 'project-a', name: 'Default Project' }} onSubmit={vi.fn()} onClose={vi.fn()} provisional />);

    expect(screen.getByRole('heading', { name: '為專案命名' })).toBeDefined();
    expect(screen.getByText('請為新的 Default Project 命名，或暫時略過。')).toBeDefined();
    expect(screen.getByRole('button', { name: '暫不命名' })).toBeDefined();
  });
});
