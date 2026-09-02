import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render, screen, waitFor } from '@testing-library/react';

const mockedQuota = {
  enforced: true,
  allowed: true,
  runs_today: 2,
  daily_limit: 5,
  new_raw_files: 4,
  min_new_raw: 1,
  already_running: false,
};

const mocks = vi.hoisted(() => ({
  currentProject: { id: 'project-a', name: 'Project A' },
  getPipelineStatus: vi.fn(),
  refreshNavCounts: vi.fn(),
}));
const testStorage = new Map<string, string>();
const storageLike = {
  getItem: (key: string): string | null => testStorage.get(key) ?? null,
  setItem: (key: string, value: string) => {
    testStorage.set(key, String(value));
  },
  removeItem: (key: string) => {
    testStorage.delete(key);
  },
};

vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api');
  return {
    ...actual,
    getPipelineStatus: mocks.getPipelineStatus,
  };
});

vi.mock('@/components/WorkspaceProvider', () => ({
  useWorkspace: () => ({
    currentProject: mocks.currentProject,
    isDemoSession: false,
    refreshNavCounts: mocks.refreshNavCounts,
  }),
}));

import { PipelineClient } from '@/components/PipelineClient';

const expectedQuotaLine = '今日執行：2/5 · 冷卻：無 · 新檔案：4';

beforeEach(() => {
  testStorage.clear();
  vi.stubGlobal('localStorage', storageLike);
  window.localStorage.setItem('locale', 'zh-TW');
  mocks.getPipelineStatus.mockResolvedValue({
    quota: mockedQuota,
  });

  render(<PipelineClient />);
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  vi.unstubAllGlobals();
  testStorage.clear();
});

describe('LWC-146: main-path zh-TW pipeline content', () => {
  it('renders localized Pipeline add-content and run text in zh-TW', async () => {
    await waitFor(() => {
      expect(document.body.textContent).toBeTruthy();
    });

    expect(screen.getByRole('heading', { name: '新增內容' })).toBeDefined();
    expect(screen.getByText('上傳 markdown 檔案或擷取網址，以建立知識資料來源。')).toBeDefined();
    expect(
      screen.getByText('可同時上傳多個檔案到 raw/ 目錄（最多 3 檔同時上傳）。'),
    ).toBeDefined();
    expect(screen.getByRole('heading', { name: '擷取 URL' })).toBeDefined();
    expect(screen.getByText('抓取網頁內容並存為原文資料。')).toBeDefined();
    expect(screen.getByText('選擇')).toBeDefined();
    expect(screen.getByRole('button', { name: '執行 Pipeline' })).toBeDefined();
    expect(
      screen.getByText('ingest（分析原始筆記）→ compile（綜合 wiki 條目）→ lint（品質檢查）→ publish（自動核准）。'),
    ).toBeDefined();
    expect(screen.getByText('啟動 OLW pipeline 以整理、編譯並上架知識條目。')).toBeDefined();

    await waitFor(() => {
      expect(screen.getByTestId('pipeline-quota-line').textContent).toBe(expectedQuotaLine);
    });

    expect(screen.queryByText('Add Content')).toBeNull();
    expect(screen.queryByText('Upload Files')).toBeNull();
    expect(screen.queryByText('Scrape URL')).toBeNull();
    expect(screen.queryByText('Run Pipeline')).toBeNull();
    expect(screen.queryByText('Select')).toBeNull();
  });
});
