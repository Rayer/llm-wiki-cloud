import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, render, screen } from '@testing-library/react';
import { renderToStaticMarkup } from 'react-dom/server';

const gates = vi.hoisted(() => ({
  auth: vi.fn(() => { throw new Error('auth bootstrap unavailable'); }),
  shell: vi.fn(() => { throw new Error('workspace unavailable'); }),
}));

vi.mock('next/font/local', () => ({
  default: () => ({ variable: '' }),
}));
vi.mock('@/lib/auth', () => ({ AuthProvider: gates.auth }));
vi.mock('@/components/Shell', () => ({ Shell: gates.shell }));

import RootLayout from '@/app/layout';
import WorkspaceLayout from '@/app/(workspace)/layout';
import LegalLayout from '@/app/(legal)/layout';
import PrivacyPage from '@/app/(legal)/privacy/page';
import TermsPage from '@/app/(legal)/terms/page';
import EnglishPrivacyPage from '@/app/(legal)/en/privacy/page';
import EnglishTermsPage from '@/app/(legal)/en/terms/page';

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});

it.each([
  [PrivacyPage, '隱私權政策', '專案內容與 AI 處理'],
  [TermsPage, '服務條款', '你提交的內容'],
] as const)('renders %s independently from unavailable workspace config, Auth, API and browser storage', (Page, title, section) => {
  const fetch = vi.fn(() => { throw new Error('API unavailable'); });
  vi.stubGlobal('fetch', fetch);
  const storage = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('storage unavailable'); });
  const content = <LegalLayout><Page /></LegalLayout>;

  // Render the actual root + route layouts, not just a page component.
  const html = renderToStaticMarkup(<RootLayout>{content}</RootLayout>);
  const document = new DOMParser().parseFromString(html, 'text/html');
  expect(document.documentElement.lang).toBe('zh-Hant');
  expect(document.querySelector('main h1')?.textContent).toBe(title);
  expect(document.querySelector('main')?.textContent).toContain(section);
  expect(document.querySelectorAll('main section').length).toBeGreaterThanOrEqual(5);
  expect(document.querySelector('main')?.textContent).toContain('由 Rayer Tung 經營的個人興趣專案');
  expect(document.querySelector('a[href="mailto:rayershih@gmail.com"]')?.textContent).toBe('rayershih@gmail.com');
  expect(document.querySelector('main')?.textContent).toContain('服務、隱私或資料刪除相關請求');
  expect(document.querySelector('main')?.textContent).toContain('自公開發布時生效');
  expect(document.querySelector('main')?.textContent).not.toMatch(/草案|尚未生效|擬議|預設設定|DeepSeek/);
  expect(document.querySelector('[role="dialog"]')).toBeNull();

  // Client mount must also work without any providers or side effects.
  render(content);
  expect(screen.getByRole('heading', { level: 1, name: title })).toBeTruthy();
  expect(screen.getByRole('heading', { name: section })).toBeTruthy();
  expect(screen.getByRole('link', { name: 'LLM Wiki Cloud home / LLM Wiki Cloud 返回首頁' }).getAttribute('href')).toBe('/');
  const contact = screen.getByRole('link', { name: 'rayershih@gmail.com' });
  expect(contact.getAttribute('href')).toBe('mailto:rayershih@gmail.com');
  contact.focus();
  expect(globalThis.document.activeElement).toBe(contact);
  expect(fetch).not.toHaveBeenCalled();
  expect(storage).not.toHaveBeenCalled();
  expect(gates.auth).not.toHaveBeenCalled();
  expect(gates.shell).not.toHaveBeenCalled();

  // Workspace startup stays behind its runtime-config loading boundary.
  const workspaceHtml = renderToStaticMarkup(<RootLayout><WorkspaceLayout>{content}</WorkspaceLayout></RootLayout>);
  const workspaceDocument = new DOMParser().parseFromString(workspaceHtml, 'text/html');
  expect(workspaceDocument.querySelector('[role="status"]')?.textContent).toContain('執行階段設定');
  expect(gates.auth).not.toHaveBeenCalled();
  expect(gates.shell).not.toHaveBeenCalled();
  expect(fetch).not.toHaveBeenCalled();
});

it.each([
  [EnglishPrivacyPage, 'Privacy Policy', 'Project content and AI processing', 'Account and sign-in information'],
  [EnglishTermsPage, 'Terms of Service', 'Content you submit', 'AI results and third-party services'],
] as const)('renders %s as fixed English policy content without workspace startup', (Page, title, section, expectedDisclosure) => {
  const fetch = vi.fn(() => { throw new Error('API unavailable'); });
  vi.stubGlobal('fetch', fetch);
  const storage = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('storage unavailable'); });
  const content = <LegalLayout><Page /></LegalLayout>;
  const html = renderToStaticMarkup(<RootLayout>{content}</RootLayout>);
  const document = new DOMParser().parseFromString(html, 'text/html');
  expect(document.querySelector('[lang="en"] h1')?.textContent).toBe(title);
  expect(document.querySelector('main article [lang="en"]')?.textContent).toContain(expectedDisclosure);
  expect(document.querySelectorAll('main section').length).toBeGreaterThanOrEqual(5);
  expect(document.querySelector('a[href="mailto:rayershih@gmail.com"]')?.textContent).toBe('rayershih@gmail.com');
  expect(document.querySelector('a[href="/privacy"]')).not.toBeNull();
  expect(document.querySelector('a[href="/terms"]')).not.toBeNull();
  expect(document.querySelector('a[href="/en/privacy"]')).not.toBeNull();
  expect(document.querySelector('a[href="/en/terms"]')).not.toBeNull();
  render(content);
  expect(screen.getByRole('heading', { level: 1, name: title })).toBeTruthy();
  expect(screen.getByRole('heading', { name: section })).toBeTruthy();
  expect(fetch).not.toHaveBeenCalled();
  expect(storage).not.toHaveBeenCalled();
  expect(gates.auth).not.toHaveBeenCalled();
  expect(gates.shell).not.toHaveBeenCalled();
});
