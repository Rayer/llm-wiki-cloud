import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { cleanup, render, screen } from '@testing-library/react';

const refreshNavCounts = vi.fn();

vi.mock('@/components/WorkspaceProvider', () => ({
  useWorkspace: () => ({ isDemoSession: false, refreshNavCounts }),
}));

import { RawUploadPanel } from '@/components/RawUploadPanel';

beforeEach(() => {
  window.localStorage.setItem('locale', 'en');
});

afterEach(() => {
  cleanup();
  window.localStorage.removeItem('locale');
  vi.clearAllMocks();
});

it.each([
  ['en', 'Select files'],
  ['zh-TW', '選擇檔案'],
])('shows the localized root upload control from the real catalog for %s', (locale, fileLabel) => {
  window.localStorage.setItem('locale', locale);

  render(<RawUploadPanel title="Upload" hint="Hint" inputId={`locale-${locale}`} />);

  expect(screen.getByText(fileLabel)).toBeDefined();
  expect(document.querySelector<HTMLInputElement>(`#locale-${locale}`)?.multiple).toBe(true);
  expect(document.querySelector('input[type="text"]')).toBeNull();
});
