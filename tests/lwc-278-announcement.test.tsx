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

  it('renders an HTTPS image with an empty alt attribute', () => {
    render(<AnnouncementBoard markdown={'![](https://www.rayer.idv.tw/blog/wp-content/uploads/2026/01/截圖-2026-01-01-下午3.06.38.png)\n\n測試一下圖片是否正常'} />);
    const image = document.querySelector('img');
    expect(image).not.toBeNull();
    expect(image?.getAttribute('alt')).toBe('');
    expect(decodeURIComponent(image?.getAttribute('src') ?? '')).toBe('https://www.rayer.idv.tw/blog/wp-content/uploads/2026/01/截圖-2026-01-01-下午3.06.38.png');
  });

  it('keeps long content bounded and hides empty content', () => {
    const { rerender } = render(<AnnouncementBoard markdown="" />);
    expect(screen.queryByRole('region')).toBeNull();
    rerender(<AnnouncementBoard markdown={'# Live\n\n' + 'A long announcement. '.repeat(100)} />);
    expect(screen.getByRole('region').className).toMatch(/max-h-|overflow-y-auto/);
  });
});

describe('LWC-278 login placement', () => {
  it('keeps the login card primary and exposes the announcement trigger separately', async () => {
    mockGetPublicConfig.mockResolvedValue({ registration_enabled: false, announcement_markdown: '# Live' });
    mockUseWorkspace.mockReturnValue({ loginOpen: true, signIn: vi.fn(), signInAsDemo: vi.fn() });
    const { LoginModal } = await import('@/components/LoginModal');
    render(<LoginModal />);
    expect(await screen.findByRole('button', { name: 'Announcement.open' })).toBeDefined();
    expect(screen.queryByRole('region')).toBeNull();
  });
});

describe('LWC-291 announcement modal', () => {
  afterEach(() => {
    cleanup();
    localStorage.clear();
    vi.clearAllMocks();
  });

  function setup(config: Record<string, unknown> = {
    registration_enabled: false,
    announcement_markdown: '# Live',
    announcement_digest: `sha256:${'a'.repeat(64)}`,
  }) {
    mockGetPublicConfig.mockResolvedValue(config);
    mockUseWorkspace.mockReturnValue({ loginOpen: true, signIn: vi.fn(), signInAsDemo: vi.fn() });
    return import('@/components/LoginModal').then(async ({ LoginModal }) => {
      let result!: ReturnType<typeof render>;
      await act(async () => {
        result = render(<LoginModal />);
      });
      return result;
    });
  }

  it('auto-opens once, suppresses the same dismissed digest, and reopens a changed digest', async () => {
    await setup();
    const modal = await screen.findByRole('dialog', { name: 'Announcement.title' });
    expect(modal).toBeDefined();
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Announcement.close' })));
    const checkbox = screen.getByRole('checkbox', { name: 'Announcement.dismiss' });
    checkbox.focus();
    fireEvent.click(checkbox);
    await waitFor(() => expect(document.activeElement).toBe(checkbox));
    fireEvent.click(screen.getByRole('button', { name: 'Announcement.close' }));
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Announcement.title' })).toBeNull());
    await waitFor(() => expect(document.activeElement?.getAttribute('autocomplete')).toBe('email'));
    expect(localStorage.getItem('llm-wiki:announcement-dismissed-digest')).toBe(`sha256:${'a'.repeat(64)}`);

    cleanup();
    await setup();
    await screen.findByRole('button', { name: 'Announcement.open' });
    expect(screen.queryByRole('dialog', { name: 'Announcement.title' })).toBeNull();

    cleanup();
    mockGetPublicConfig.mockResolvedValue({
      registration_enabled: false,
      announcement_markdown: '# New',
      announcement_digest: `sha256:${'b'.repeat(64)}`,
    });
    await setup({
      registration_enabled: false,
      announcement_markdown: '# New',
      announcement_digest: `sha256:${'b'.repeat(64)}`,
    });
    expect(await screen.findByRole('dialog', { name: 'Announcement.title' })).toBeDefined();
  });

  it('writes for checked Escape and manual checked close, never backdrop, and resets manual checkbox', async () => {
    await setup();
    const checkbox = await screen.findByRole('checkbox', { name: 'Announcement.dismiss' });
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Announcement.close' })));
    checkbox.focus();
    fireEvent.click(checkbox);
    await waitFor(() => expect(document.activeElement).toBe(checkbox));
    fireEvent.click(screen.getByRole('button', { name: 'Announcement.close' }));
    expect(localStorage.getItem('llm-wiki:announcement-dismissed-digest')).toMatch(/^sha256:a/);

    fireEvent.click(screen.getByRole('button', { name: 'Announcement.open' }));
    expect((screen.getByRole('checkbox', { name: 'Announcement.dismiss' }) as HTMLInputElement).checked).toBe(false);
    localStorage.removeItem('llm-wiki:announcement-dismissed-digest');
    const manualCheckbox = screen.getByRole('checkbox', { name: 'Announcement.dismiss' });
    manualCheckbox.focus();
    fireEvent.click(manualCheckbox);
    await waitFor(() => expect(document.activeElement).toBe(manualCheckbox));
    fireEvent.keyDown(document, { key: 'Escape' });
    expect(localStorage.getItem('llm-wiki:announcement-dismissed-digest')).toBe(`sha256:${'a'.repeat(64)}`);

    fireEvent.click(screen.getByRole('button', { name: 'Announcement.open' }));
    expect((screen.getByRole('checkbox', { name: 'Announcement.dismiss' }) as HTMLInputElement).checked).toBe(false);
    const dialog = screen.getByRole('dialog', { name: 'Announcement.title' });
    fireEvent.click(dialog.parentElement as HTMLElement);
    expect(screen.getByRole('dialog', { name: 'Announcement.title' })).toBeDefined();
  });

  it('stores a valid digest when a manually opened announcement is checked and closed', async () => {
    await setup();
    await screen.findByRole('dialog', { name: 'Announcement.title' });
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Announcement.close' })));
    fireEvent.click(screen.getByRole('button', { name: 'Announcement.close' }));
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Announcement.title' })).toBeNull());
    fireEvent.click(screen.getByRole('button', { name: 'Announcement.open' }));
    const checkbox = screen.getByRole('checkbox', { name: 'Announcement.dismiss' });
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Announcement.close' })));
    checkbox.focus();
    fireEvent.click(checkbox);
    await waitFor(() => expect(document.activeElement).toBe(checkbox));
    fireEvent.click(screen.getByRole('button', { name: 'Announcement.close' }));
    expect(localStorage.getItem('llm-wiki:announcement-dismissed-digest')).toBe(`sha256:${'a'.repeat(64)}`);
  });

  it('keeps manual trigger for missing or malformed digests and empty content hidden', async () => {
    await setup({ registration_enabled: false, announcement_markdown: '# Live', announcement_digest: null });
    expect(screen.queryByRole('dialog', { name: 'Announcement.title' })).toBeNull();
    expect(await screen.findByRole('button', { name: 'Announcement.open' })).toBeDefined();
    cleanup();
    await setup({ registration_enabled: false, announcement_markdown: '# Live', announcement_digest: 'sha256:not-valid' });
    expect(screen.queryByRole('dialog', { name: 'Announcement.title' })).toBeNull();
    cleanup();
    await setup({ registration_enabled: false, announcement_markdown: '', announcement_digest: `sha256:${'a'.repeat(64)}` });
    expect(screen.queryByRole('button', { name: 'Announcement.open' })).toBeNull();
  });

  it('does not block login or overwrite storage on API and storage failures', async () => {
    const digest = `sha256:${'a'.repeat(64)}`;
    localStorage.setItem('llm-wiki:announcement-dismissed-digest', digest);
    let rejectPublicConfig!: (error: Error) => void;
    mockGetPublicConfig.mockReturnValue(new Promise((_, reject) => { rejectPublicConfig = reject; }));
    mockUseWorkspace.mockReturnValue({ loginOpen: true, signIn: vi.fn(), signInAsDemo: vi.fn() });
    const { LoginModal } = await import('@/components/LoginModal');
    await act(async () => {
      render(<LoginModal />);
      rejectPublicConfig(new Error('offline'));
      await Promise.resolve();
    });
    expect(await screen.findByRole('textbox', { name: 'Login.email' })).toBeDefined();

    cleanup();
    localStorage.setItem('llm-wiki:announcement-dismissed-digest', 'sha256:old');
    const setItem = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('blocked'); });
    await setup({ registration_enabled: false, announcement_markdown: '# Live', announcement_digest: digest });
    await screen.findByRole('dialog', { name: 'Announcement.title' });
    fireEvent.click(screen.getByRole('checkbox', { name: 'Announcement.dismiss' }));
    fireEvent.click(screen.getByRole('button', { name: 'Announcement.close' }));
    expect(localStorage.getItem('llm-wiki:announcement-dismissed-digest')).toBe('sha256:old');
    setItem.mockRestore();
  });

  it('focuses the close control on open and restores the manual trigger after manual close', async () => {
    await setup({ registration_enabled: false, announcement_markdown: '# Live', announcement_digest: null });
    const trigger = await screen.findByRole('button', { name: 'Announcement.open' });
    fireEvent.click(trigger);
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Announcement.close' })));
    fireEvent.click(screen.getByRole('button', { name: 'Announcement.close' }));
    await waitFor(() => expect(document.activeElement).toBe(trigger));
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
