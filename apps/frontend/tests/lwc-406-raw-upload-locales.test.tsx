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
  ['en', 'Select files', 'Destination directory under raw/ (optional)'],
  ['zh-TW', '選擇檔案', 'raw/ 下的目標資料夾（選填）'],
])('shows localized upload labels from the real catalog for %s', (locale, fileLabel, directoryLabel) => {
  window.localStorage.setItem('locale', locale);

  render(<RawUploadPanel title="Upload" hint="Hint" inputId={`locale-${locale}`} />);

  expect(screen.getByText(fileLabel)).toBeDefined();
  expect(screen.getByLabelText(directoryLabel)).toBeDefined();
});
