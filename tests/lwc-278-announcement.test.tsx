import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { AnnouncementBoard } from '@/components/AnnouncementBoard';

const { mockGetPublicConfig, mockUseWorkspace } = vi.hoisted(() => ({
  mockGetPublicConfig: vi.fn(),
  mockUseWorkspace: vi.fn(),
}));

vi.mock('@/lib/api', () => ({ getPublicConfig: mockGetPublicConfig }));
vi.mock('@/lib/i18n', () => ({ useLocale: () => ({ t: (key: string) => key }) }));
vi.mock('@/components/RegisterModal', () => ({ RegisterModal: () => null }));
vi.mock('@/components/WorkspaceProvider', () => ({ useWorkspace: mockUseWorkspace }));

describe('LWC-278 announcement board', () => {
  afterEach(() => cleanup());
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
