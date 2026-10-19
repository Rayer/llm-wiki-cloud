import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

if (!(React as { act?: (callback: () => unknown) => Promise<unknown> | unknown }).act) {
  Object.defineProperty(React, 'act', {
    configurable: true,
    value: (callback: () => unknown) => Promise.resolve(callback()),
  });
}

const { act, cleanup, fireEvent, render, screen, waitFor, within } = await import('@testing-library/react');

const mocks = vi.hoisted(() => ({
  addProject: vi.fn(),
  closeNewProject: vi.fn(),
  confirmNavigation: vi.fn(),
  push: vi.fn(),
  user: { id: 'user-1' } as { id: string } | null,
  sessionEpoch: 1,
  open: true,
}));

vi.mock('next/navigation', () => ({ useRouter: () => ({ push: mocks.push }) }));
vi.mock('@/components/WorkspaceProvider', () => ({
  useWorkspace: () => ({
    newProjectOpen: mocks.open,
    closeNewProject: mocks.closeNewProject,
    addProject: mocks.addProject,
    user: mocks.user,
  }),
}));
vi.mock('@/lib/auth', () => ({ useAuth: () => ({ sessionEpoch: mocks.sessionEpoch }) }));
vi.mock('@/components/NavigationBlocker', () => ({
  useNavigationBlocker: () => ({ confirmNavigation: mocks.confirmNavigation }),
}));

import { NewProjectModal } from '@/components/NewProjectModal';

const newProject = { id: 'project-new', name: 'Research' };

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => { resolve = resolvePromise; });
  return { promise, resolve };
}

beforeEach(() => {
  localStorage.setItem('locale', 'en');
  mocks.addProject.mockResolvedValue(newProject);
  mocks.closeNewProject.mockReset();
  mocks.confirmNavigation.mockReturnValue(true);
  mocks.push.mockReset();
  mocks.user = { id: 'user-1' };
  mocks.sessionEpoch = 1;
  mocks.open = true;
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe('LWC-406 project creation', () => {
  it.each([
    ['en', 'Create project', 'Project name', 'Learn how project settings work', 'Back to project creation'],
    ['zh-TW', '建立專案', '專案名稱', '了解專案設定方式', '返回建立專案'],
  ] as const)('reuses the Profile explanation and preserves the project name in %s', async (locale, title, nameLabel, helpLabel, backLabel) => {
    localStorage.setItem('locale', locale);
    render(<NewProjectModal />);
    fireEvent.change(screen.getByRole('textbox', { name: nameLabel }), { target: { value: 'Café research' } });

    expect(screen.getByRole('dialog', { name: title })).toBeDefined();
    expect(screen.queryByRole('textbox', { name: /Requirement/ })).toBeNull();
    expect(screen.getByText(locale === 'en'
      ? 'Each project is a separate knowledge workspace. After you create it, you can set optional organization and search preferences, or add data directly.'
      : '每個專案是一個獨立的知識工作空間。建立後可選擇設定整理與搜尋偏好，也可以直接加入資料。')).toBeDefined();

    fireEvent.click(screen.getByRole('button', { name: helpLabel }));
    expect(screen.getByRole('heading', { name: locale === 'en' ? 'How Profile shapes organization and search' : 'Profile 如何影響整理與搜尋' })).toBeDefined();
    expect(screen.queryByRole('button', { name: locale === 'en' ? 'Set my preferences' : '設定我的偏好' })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: backLabel }));

    expect((screen.getByRole('textbox', { name: nameLabel }) as HTMLInputElement).value).toBe('Café research');
    expect(mocks.addProject).not.toHaveBeenCalled();
    expect(mocks.push).not.toHaveBeenCalled();
  });

  it.each([
    ['en', 'x', 'Close create project dialog', 'Project name', 'Learn how project settings work'],
    ['en', 'escape', 'Close create project dialog', 'Project name', 'Learn how project settings work'],
    ['en', 'backdrop', 'Close create project dialog', 'Project name', 'Learn how project settings work'],
    ['zh-TW', 'x', '關閉建立專案視窗', '專案名稱', '了解專案設定方式'],
    ['zh-TW', 'escape', '關閉建立專案視窗', '專案名稱', '了解專案設定方式'],
    ['zh-TW', 'backdrop', '關閉建立專案視窗', '專案名稱', '了解專案設定方式'],
  ] as const)('reopens creation in %s after a %s close', async (locale, closeMethod, closeLabel, nameLabel, helpLabel) => {
    localStorage.setItem('locale', locale);
    const view = render(<NewProjectModal />);
    fireEvent.change(screen.getByRole('textbox', { name: nameLabel }), { target: { value: 'Café research' } });
    fireEvent.click(screen.getByRole('button', { name: helpLabel }));
    const stages = within(screen.getByRole('navigation')).getAllByRole('button');
    fireEvent.click(stages[2]);
    expect(stages[2].getAttribute('aria-current')).toBe('step');

    if (closeMethod === 'x') {
      fireEvent.click(screen.getByRole('button', { name: closeLabel }));
    } else if (closeMethod === 'escape') {
      fireEvent.keyDown(window, { key: 'Escape' });
    } else {
      fireEvent.mouseDown(screen.getByRole('dialog').parentElement!);
    }

    expect(mocks.closeNewProject).toHaveBeenCalledOnce();
    mocks.open = false;
    view.rerender(<NewProjectModal />);
    expect(screen.queryByRole('dialog')).toBeNull();
    mocks.open = true;
    view.rerender(<NewProjectModal />);

    const nameInput = screen.getByRole('textbox', { name: nameLabel });
    await waitFor(() => expect(document.activeElement).toBe(nameInput));
    expect((nameInput as HTMLInputElement).value).toBe('Café research');
    fireEvent.click(screen.getByRole('button', { name: helpLabel }));
    expect(within(screen.getByRole('navigation')).getAllByRole('button')[0].getAttribute('aria-current')).toBe('step');
    expect(mocks.addProject).not.toHaveBeenCalled();
    expect(mocks.push).not.toHaveBeenCalled();
  });

  it('keeps a created project available for navigation retry after close and reopen', async () => {
    mocks.push.mockImplementationOnce(() => { throw new Error('transition unavailable'); });
    const view = render(<NewProjectModal />);
    fireEvent.change(screen.getByRole('textbox', { name: 'Project name' }), { target: { value: 'Research' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create project' }));
    expect(await screen.findByRole('button', { name: 'Open project settings' })).toBeDefined();

    fireEvent.click(screen.getByRole('button', { name: 'Close create project dialog' }));
    mocks.open = false;
    view.rerender(<NewProjectModal />);
    mocks.open = true;
    view.rerender(<NewProjectModal />);

    expect(screen.getByRole('textbox', { name: 'Project name' })).toHaveProperty('disabled', true);
    fireEvent.click(screen.getByRole('button', { name: 'Open project settings' }));
    await waitFor(() => expect(mocks.push).toHaveBeenCalledTimes(2));
    expect(mocks.addProject).toHaveBeenCalledOnce();
  });

  it('creates without Profile reads or saves and navigates to the Profile page', async () => {
    render(<NewProjectModal />);
    fireEvent.change(screen.getByRole('textbox', { name: 'Project name' }), { target: { value: 'Research' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create project' }));

    await waitFor(() => expect(mocks.addProject).toHaveBeenCalledTimes(1));
    expect(mocks.addProject).toHaveBeenCalledWith('Research');
    expect(mocks.push).toHaveBeenCalledWith('/profile');
    expect(mocks.closeNewProject).toHaveBeenCalledTimes(1);
  });

  it('keeps the name and error after a create failure without navigating', async () => {
    mocks.addProject.mockRejectedValueOnce(new Error('name already exists'));
    render(<NewProjectModal />);
    fireEvent.change(screen.getByRole('textbox', { name: 'Project name' }), { target: { value: 'Research' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create project' }));

    expect(await screen.findByRole('alert')).toHaveProperty('textContent', 'name already exists');
    expect((screen.getByRole('textbox', { name: 'Project name' }) as HTMLInputElement).value).toBe('Research');
    expect(mocks.push).not.toHaveBeenCalled();
  });

  it('retries navigation for the created project without sending a second create', async () => {
    mocks.push.mockImplementationOnce(() => { throw new Error('transition unavailable'); });
    render(<NewProjectModal />);
    fireEvent.change(screen.getByRole('textbox', { name: 'Project name' }), { target: { value: 'Research' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create project' }));

    expect(await screen.findByRole('button', { name: 'Open project settings' })).toBeDefined();
    expect(screen.getByRole('alert').textContent).toContain('Project created');
    fireEvent.click(screen.getByRole('button', { name: 'Open project settings' }));

    await waitFor(() => expect(mocks.push).toHaveBeenCalledTimes(2));
    expect(mocks.addProject).toHaveBeenCalledTimes(1);
    expect(mocks.push).toHaveBeenLastCalledWith('/profile');
  });

  it('honors the existing dirty-navigation guard before creating', async () => {
    mocks.confirmNavigation.mockReturnValue(false);
    render(<NewProjectModal />);
    fireEvent.change(screen.getByRole('textbox', { name: 'Project name' }), { target: { value: 'Research' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create project' }));

    expect(mocks.confirmNavigation).toHaveBeenCalledOnce();
    expect(mocks.addProject).not.toHaveBeenCalled();
    expect(mocks.push).not.toHaveBeenCalled();
  });

  it('ignores a late create after the session changes', async () => {
    const pendingCreate = deferred<typeof newProject>();
    mocks.addProject.mockReturnValue(pendingCreate.promise);
    const view = render(<NewProjectModal />);
    fireEvent.change(screen.getByRole('textbox', { name: 'Project name' }), { target: { value: 'Research' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create project' }));

    mocks.user = { id: 'user-2' };
    mocks.sessionEpoch = 2;
    view.rerender(<NewProjectModal />);
    await act(async () => {
      pendingCreate.resolve(newProject);
      await Promise.resolve();
    });

    expect(mocks.addProject).toHaveBeenCalledOnce();
    expect(mocks.push).not.toHaveBeenCalled();
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('ignores a late create after the modal is closed and reopened', async () => {
    const pendingCreate = deferred<typeof newProject>();
    mocks.addProject.mockReturnValue(pendingCreate.promise);
    const view = render(<NewProjectModal />);
    fireEvent.change(screen.getByRole('textbox', { name: 'Project name' }), { target: { value: 'Research' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create project' }));

    mocks.open = false;
    view.rerender(<NewProjectModal />);
    mocks.open = true;
    view.rerender(<NewProjectModal />);
    await act(async () => {
      pendingCreate.resolve(newProject);
      await Promise.resolve();
    });

    expect(mocks.addProject).toHaveBeenCalledOnce();
    expect(mocks.push).not.toHaveBeenCalled();
    expect((screen.getByRole('textbox', { name: 'Project name' }) as HTMLInputElement).value).toBe('Research');
  });
});
