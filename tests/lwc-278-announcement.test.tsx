import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { AnnouncementBoard } from '@/components/AnnouncementBoard';

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

const {
  mockGetAdminProjects,
  mockGetAdminSettings,
  mockGetAdminUsers,
  mockGetPublicConfig,
  mockPublishAnnouncement,
  mockUseAuth,
  mockUseWorkspace,
} = vi.hoisted(() => ({
  mockGetAdminProjects: vi.fn(),
  mockGetAdminSettings: vi.fn(),
  mockGetAdminUsers: vi.fn(),
  mockGetPublicConfig: vi.fn(),
  mockPublishAnnouncement: vi.fn(),
  mockUseAuth: vi.fn(),
  mockUseWorkspace: vi.fn(),
}));

vi.mock('@/lib/api', () => ({
  ApiError: class ApiError extends Error {},
  clearPublicConfigCache: vi.fn(),
  deleteAdminProject: vi.fn(),
  deleteAdminUser: vi.fn(),
  getAdminProjects: mockGetAdminProjects,
  getAdminSettings: mockGetAdminSettings,
  getAdminUsers: mockGetAdminUsers,
  getPublicConfig: mockGetPublicConfig,
  publishAnnouncement: mockPublishAnnouncement,
  rebuildAdminProjectIndex: vi.fn(),
  renameAdminProject: vi.fn(),
  triggerAdminProjectPipeline: vi.fn(),
  updateAdminSettings: vi.fn(),
  updateAdminUserRole: vi.fn(),
}));
vi.mock('@/lib/auth', () => ({ useAuth: mockUseAuth }));
vi.mock('@/lib/i18n', () => ({
  useLocale: () => ({ t: (key: string) => key }),
  useT: () => ({ t: (key: string) => key }),
}));
vi.mock('@/components/RegisterModal', () => ({ RegisterModal: () => null }));
vi.mock('@/components/WorkspaceProvider', () => ({ useWorkspace: mockUseWorkspace }));

describe('LWC-278 announcement board', () => {
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });
  it('renders the safe markdown subset and safe image attributes', () => {
    render(<AnnouncementBoard markdown={'# Hello\n\n**Safe** [docs](https://example.com) ![A chart](https://img.example/chart.png)\n\n<script>alert(1)</script> [bad](javascript:alert(1)) ![bad](data:image/svg+xml;base64,abc)'} />);
    expect(screen.getByRole('heading', { name: 'Hello' })).toBeDefined();
    expect(screen.getByText('Safe')).toBeDefined();
    expect(screen.getByRole('link', { name: 'docs' }).getAttribute('href')).toBe('https://example.com');
    const image = screen.getByRole('img', { name: 'A chart' });
    expect(image.getAttribute('src')).toBe('https://img.example/chart.png');
    expect(image.getAttribute('loading')).toBe('lazy');
    expect(image.getAttribute('decoding')).toBe('async');
    expect(image.getAttribute('referrerpolicy')).toBe('no-referrer');
    expect(document.querySelector('script')).toBeNull();
    expect(document.querySelector('img[src^="data:"]')).toBeNull();
  });

  it('keeps long content bounded and hides empty content', () => {
    const { rerender } = render(<AnnouncementBoard markdown="" />);
    expect(screen.queryByRole('region')).toBeNull();
    rerender(<AnnouncementBoard markdown={'# Live\n\n' + 'A long announcement. '.repeat(100)} />);
    expect(screen.getByRole('region').className).toMatch(/max-h-|overflow-y-auto/);
  });
});

describe('LWC-278 login placement', () => {
  it('places the announcement before the existing login form', async () => {
    mockGetPublicConfig.mockResolvedValue({ registration_enabled: false, announcement_markdown: '# Live' });
    mockUseWorkspace.mockReturnValue({ loginOpen: true, signIn: vi.fn(), signInAsDemo: vi.fn() });
    const { LoginModal } = await import('@/components/LoginModal');
    render(<LoginModal />);
    await waitFor(() => expect(screen.getByRole('region')).toBeDefined());
    const dialog = screen.getByRole('dialog');
    expect(dialog.textContent?.indexOf('Live')).toBeLessThan(dialog.textContent?.indexOf('Login.email') ?? -1);
  });
});

describe('LWC-278 direct publish admin flow', () => {
  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
    vi.restoreAllMocks();
  });

  it('loads the published Markdown, previews edits, and publishes the current text', async () => {
    mockUseAuth.mockReturnValue({ hydrated: true, user: { role: 'admin' } });
    mockGetAdminProjects.mockResolvedValue([]);
    mockGetAdminUsers.mockResolvedValue([]);
    mockGetAdminSettings.mockResolvedValue({ registration_enabled: true, announcement_markdown: '# Published' });
    mockPublishAnnouncement.mockResolvedValue({ registration_enabled: true, announcement_markdown: '# Edited' });

    const { AdminClient } = await import('@/components/AdminClient');
    const { NavigationBlockerProvider } = await import('@/components/NavigationBlocker');
    render(<NavigationBlockerProvider><AdminClient /></NavigationBlockerProvider>);
    fireEvent.click(await screen.findByRole('button', { name: 'Settings' }));

    const editor = await screen.findByRole('textbox', { name: 'Announcement Markdown' });
    expect((editor as HTMLTextAreaElement).value).toBe('# Published');
    fireEvent.change(editor, { target: { value: '# Edited' } });
    expect(screen.getByRole('heading', { name: 'Edited' })).toBeDefined();

    fireEvent.click(screen.getByRole('button', { name: 'Publish' }));
    await waitFor(() => expect(mockPublishAnnouncement).toHaveBeenCalledWith('# Edited'));
  });

  it('disables Publish when unchanged and while the direct publish is pending', async () => {
    mockUseAuth.mockReturnValue({ hydrated: true, user: { role: 'admin' } });
    mockGetAdminProjects.mockResolvedValue([]);
    mockGetAdminUsers.mockResolvedValue([]);
    mockGetAdminSettings.mockResolvedValue({ registration_enabled: true, announcement_markdown: '# Published' });
    let resolvePublish!: (value: unknown) => void;
    mockPublishAnnouncement.mockReturnValue(new Promise((resolve) => { resolvePublish = resolve; }));

    const { AdminClient } = await import('@/components/AdminClient');
    const { NavigationBlockerProvider } = await import('@/components/NavigationBlocker');
    render(<NavigationBlockerProvider><AdminClient /></NavigationBlockerProvider>);
    fireEvent.click(await screen.findByRole('button', { name: 'Settings' }));
    const editor = await screen.findByRole('textbox', { name: 'Announcement Markdown' });
    const publish = screen.getByRole('button', { name: 'Publish' }) as HTMLButtonElement;
    expect(publish.disabled).toBe(true);

    fireEvent.change(editor, { target: { value: '# Edited' } });
    expect(publish.disabled).toBe(false);
    fireEvent.click(publish);
    await waitFor(() => expect(publish.disabled).toBe(true));
    await act(async () => {
      resolvePublish({ registration_enabled: true, announcement_markdown: '# Edited' });
    });
    await waitFor(() => expect(publish.disabled).toBe(true));
  });

  it('keeps the editor dirty and reports an invalid publish response', async () => {
    mockUseAuth.mockReturnValue({ hydrated: true, user: { role: 'admin' } });
    mockGetAdminProjects.mockResolvedValue([]);
    mockGetAdminUsers.mockResolvedValue([]);
    mockGetAdminSettings.mockResolvedValue({ registration_enabled: true, announcement_markdown: '# Published' });
    mockPublishAnnouncement.mockResolvedValue({ registration_enabled: true });

    const { AdminClient } = await import('@/components/AdminClient');
    const { NavigationBlockerProvider } = await import('@/components/NavigationBlocker');
    render(<NavigationBlockerProvider><AdminClient /></NavigationBlockerProvider>);
    fireEvent.click(await screen.findByRole('button', { name: 'Settings' }));
    const editor = await screen.findByRole('textbox', { name: 'Announcement Markdown' });
    fireEvent.change(editor, { target: { value: '# Edited' } });
    fireEvent.click(screen.getByRole('button', { name: 'Publish' }));

    await waitFor(() => expect(screen.getByText('Invalid announcement publish response')).toBeDefined());
    expect((editor as HTMLTextAreaElement).value).toBe('# Edited');
    expect((screen.getByRole('button', { name: 'Publish' }) as HTMLButtonElement).disabled).toBe(false);
    const unload = new Event('beforeunload', { cancelable: true });
    window.dispatchEvent(unload);
    expect(unload.defaultPrevented).toBe(true);
  });

  it('confirms before publishing empty content', async () => {
    mockUseAuth.mockReturnValue({ hydrated: true, user: { role: 'admin' } });
    mockGetAdminProjects.mockResolvedValue([]);
    mockGetAdminUsers.mockResolvedValue([]);
    mockGetAdminSettings.mockResolvedValue({ registration_enabled: true, announcement_markdown: '# Published' });
    mockPublishAnnouncement.mockResolvedValue({ registration_enabled: true, announcement_markdown: '' });
    vi.spyOn(window, 'confirm').mockReturnValue(false);

    const { AdminClient } = await import('@/components/AdminClient');
    const { NavigationBlockerProvider } = await import('@/components/NavigationBlocker');
    render(<NavigationBlockerProvider><AdminClient /></NavigationBlockerProvider>);
    fireEvent.click(await screen.findByRole('button', { name: 'Settings' }));
    fireEvent.change(await screen.findByRole('textbox', { name: 'Announcement Markdown' }), { target: { value: '' } });
    fireEvent.click(screen.getByRole('button', { name: 'Publish' }));

    expect(window.confirm).toHaveBeenCalled();
    expect(mockPublishAnnouncement).not.toHaveBeenCalled();
  });

  it('publishes confirmed empty content and clears the navigation blocker on success', async () => {
    mockUseAuth.mockReturnValue({ hydrated: true, user: { role: 'admin' } });
    mockGetAdminProjects.mockResolvedValue([]);
    mockGetAdminUsers.mockResolvedValue([]);
    mockGetAdminSettings.mockResolvedValue({ registration_enabled: true, announcement_markdown: '# Published' });
    mockPublishAnnouncement.mockResolvedValue({ registration_enabled: true, announcement_markdown: '' });
    vi.spyOn(window, 'confirm').mockReturnValue(true);

    const { AdminClient } = await import('@/components/AdminClient');
    const { NavigationBlockerProvider } = await import('@/components/NavigationBlocker');
    render(<NavigationBlockerProvider><AdminClient /></NavigationBlockerProvider>);
    fireEvent.click(await screen.findByRole('button', { name: 'Settings' }));
    fireEvent.change(await screen.findByRole('textbox', { name: 'Announcement Markdown' }), { target: { value: '' } });
    fireEvent.click(screen.getByRole('button', { name: 'Publish' }));
    await waitFor(() => expect(mockPublishAnnouncement).toHaveBeenCalledWith(''));
    await waitFor(() => {
      const unload = new Event('beforeunload', { cancelable: true });
      window.dispatchEvent(unload);
      expect(unload.defaultPrevented).toBe(false);
    });
  });

  it('uses the existing navigation blocker while the editor is dirty', async () => {
    mockUseAuth.mockReturnValue({ hydrated: true, user: { role: 'admin' } });
    mockGetAdminProjects.mockResolvedValue([]);
    mockGetAdminUsers.mockResolvedValue([]);
    mockGetAdminSettings.mockResolvedValue({ registration_enabled: true, announcement_markdown: '# Published' });
    const { NavigationBlockerProvider, NavigationLink } = await import('@/components/NavigationBlocker');
    const { AdminClient } = await import('@/components/AdminClient');
    vi.spyOn(window, 'confirm').mockReturnValue(false);

    render(
      <NavigationBlockerProvider>
        <AdminClient />
        <NavigationLink href="/sources">Sources</NavigationLink>
      </NavigationBlockerProvider>,
    );
    fireEvent.click(await screen.findByRole('button', { name: 'Settings' }));
    fireEvent.change(await screen.findByRole('textbox', { name: 'Announcement Markdown' }), { target: { value: '# Edited' } });
    const unload = new Event('beforeunload', { cancelable: true });
    window.dispatchEvent(unload);
    expect(unload.defaultPrevented).toBe(true);

    fireEvent.click(screen.getByRole('link', { name: 'Sources' }));
    expect(window.confirm).toHaveBeenCalled();
  });
});
