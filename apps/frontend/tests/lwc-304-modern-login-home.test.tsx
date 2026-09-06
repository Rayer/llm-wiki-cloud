import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { act } from 'react';
import type { ApiStatus } from '@/lib/api';

if (!(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT) {
  (globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
}

const mocks = vi.hoisted(() => ({
  currentProject: { id: 'project-a', name: 'Project A' },
  getConcepts: vi.fn(),
  getPublicConfig: vi.fn(),
  getStatus: vi.fn(),
  loginOpen: true,
  searchWiki: vi.fn(),
  signIn: vi.fn(),
  signInAsDemo: vi.fn(),
}));

vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api');
  return {
    ...actual,
    getConcepts: mocks.getConcepts,
    getPublicConfig: mocks.getPublicConfig,
    getStatus: mocks.getStatus,
    searchWiki: mocks.searchWiki,
  };
});

vi.mock('@/lib/i18n', () => ({
  useLocale: () => ({ t: (key: string) => key }),
  useT: () => ({ t: (key: string) => key }),
}));

vi.mock('@/components/WorkspaceProvider', () => ({
  useWorkspace: () => ({
    currentProject: mocks.currentProject,
    loginOpen: mocks.loginOpen,
    signIn: mocks.signIn,
    signInAsDemo: mocks.signInAsDemo,
  }),
}));

import { HomeClient } from '@/components/HomeClient';
import { LoginModal } from '@/components/LoginModal';

const SEARCH_RESPONSE = { results: [], aiAnswer: '', citations: [] };

function status(overrides: Partial<ApiStatus> = {}) {
  return {
    sourcesCount: 2,
    conceptsCount: 3,
    rawCount: 1,
    suggestedQueries: ['starter query', 'modern suggestion'],
    raw: {},
    ...overrides,
  } as ApiStatus;
}

beforeEach(() => {
  localStorage.clear();
  mocks.currentProject = { id: 'project-a', name: 'Project A' };
  mocks.getConcepts.mockResolvedValue([]);
  mocks.getPublicConfig.mockResolvedValue({ registration_enabled: false });
  mocks.getStatus.mockResolvedValue(status());
  mocks.loginOpen = true;
  mocks.searchWiki.mockResolvedValue(SEARCH_RESPONSE);
  mocks.signIn.mockResolvedValue(undefined);
  mocks.signInAsDemo.mockResolvedValue(undefined);
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe('LWC-304 modern login and knowledge home', () => {
  it('renders the split editorial LoginModal and submits named credentials', async () => {
    render(<LoginModal />);

    const dialog = screen.getByRole('dialog', { name: 'Login.title' });
    const overlay = dialog.parentElement as HTMLElement;
    const editorialPanel = dialog.querySelector<HTMLElement>('section[aria-hidden="true"]');
    const email = screen.getByRole('textbox', { name: 'Login.email' }) as HTMLInputElement;
    const password = screen.getByLabelText('Login.password') as HTMLInputElement;

    expect(dialog.className).toContain('md:grid-cols-[minmax(0,1.15fr)_minmax(22rem,0.85fr)]');
    expect(dialog.className).toContain('sm:max-h-[calc(100dvh-3rem)]');
    expect(overlay.className).toContain('p-3');
    expect(overlay.className).toContain('sm:p-6');
    expect(editorialPanel).not.toBeNull();
    expect(editorialPanel?.className).toContain('hidden');
    expect(editorialPanel?.className).toContain('md:flex');
    expect(email.getAttribute('name')).toBe('email');
    expect(email.getAttribute('autocomplete')).toBe('email');
    expect(email.getAttribute('spellcheck')).toBe('false');
    expect(password.getAttribute('name')).toBe('password');
    expect(password.getAttribute('autocomplete')).toBe('current-password');

    await act(async () => {
      fireEvent.change(email, { target: { value: ' person@example.com ' } });
      fireEvent.change(password, { target: { value: 'secret' } });
      fireEvent.submit(screen.getByRole('button', { name: 'Login.signIn' }).closest('form') as HTMLFormElement);
    });

    await waitFor(() => expect(mocks.signIn).toHaveBeenCalledWith('person@example.com', 'secret'));
  });

  it('renders the HomeClient query composer and submits its selected mode', async () => {
    render(<HomeClient />);

    await waitFor(() => expect(mocks.getStatus).toHaveBeenCalledTimes(1));
    const query = screen.getByRole('textbox', { name: 'Demo.search' }) as HTMLInputElement;
    const search = screen.getByRole('button', { name: 'Demo.search' });
    const form = search.closest('form') as HTMLFormElement;
    const composer = form.firstElementChild as HTMLElement;
    const controls = form.children[1] as HTMLElement;

    expect(form.className).toContain('rounded-[var(--radius-lg)]');
    expect(composer.className).toContain('flex');
    expect(composer.className).toContain('items-center');
    expect(controls.className).toContain('flex-col');
    expect(controls.className).toContain('sm:flex-row');
    expect(query.getAttribute('name')).toBe('query');
    expect(query.getAttribute('autocomplete')).toBe('off');
    expect(query.className).toContain('text-base');
    expect(query.className).toContain('min-w-0');
    expect(query.compareDocumentPosition(search) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
    expect(search.className).toContain('rounded-lg');

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'modern suggestion' }));
    });
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Demo.full' }));
    });
    await act(async () => {
      fireEvent.click(search);
    });

    expect(query.value).toBe('modern suggestion');
    expect(mocks.searchWiki).toHaveBeenCalledOnce();
    expect(mocks.searchWiki).toHaveBeenCalledWith('modern suggestion', 'full');
  });

  it('gives rendered search results and citations visible keyboard-focus affordances', async () => {
    mocks.searchWiki.mockResolvedValue({
      results: [{
        id: 'focus-result',
        slug: 'focus-result',
        title: 'Focus Result',
        type: 'concept',
        excerpt: 'A result that opens its citation preview.',
        content: '',
      }],
      aiAnswer: 'See [Focus Citation].',
      citations: [{ text: 'Focus Citation', type: 'concept', id: 'focus-citation', slug: 'focus-citation', path: '' }],
    });

    render(<HomeClient />);
    const query = await screen.findByRole('textbox', { name: 'Demo.search' });
    fireEvent.change(query, { target: { value: 'focus' } });
    fireEvent.click(screen.getByRole('button', { name: 'Demo.search' }));

    const result = await screen.findByRole('button', { name: /Focus Result/ });
    const inlineCitation = screen.getByRole('button', { name: 'Focus Citation' });
    const citation = screen.getByRole('button', { name: 'Demo.openCitation' });
    result.focus();
    citation.focus();

    expect(result.className).toContain('focus-visible:outline');
    expect(inlineCitation.className).toContain('focus-visible:outline');
    expect(citation.className).toContain('focus-visible:outline');
    expect(document.activeElement).toBe(citation);
  });
});
