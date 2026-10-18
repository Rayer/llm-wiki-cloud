import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { CLIReleaseManifest } from '@/lib/cli-release-metadata';

const mocks = vi.hoisted(() => ({ loadManifest: vi.fn(), writeText: vi.fn() }));

const platformAssets = [
  {
    target: 'darwin/arm64',
    os: 'darwin',
    arch: 'arm64',
    assetName: 'lwc-sync_1.0.0_darwin_arm64.tar.gz',
    href: 'https://storage.googleapis.com/llm-wiki-frontend-config-dev/cli/versions/1.0.0/lwc-sync_1.0.0_darwin_arm64.tar.gz',
    labels: { en: 'macOS (Apple Silicon)', 'zh-TW': 'macOS（Apple Silicon）' },
  },
  {
    target: 'darwin/amd64',
    os: 'darwin',
    arch: 'amd64',
    assetName: 'lwc-sync_1.0.0_darwin_amd64.tar.gz',
    href: 'https://storage.googleapis.com/llm-wiki-frontend-config-dev/cli/versions/1.0.0/lwc-sync_1.0.0_darwin_amd64.tar.gz',
    labels: { en: 'macOS (Intel)', 'zh-TW': 'macOS（Intel）' },
  },
  {
    target: 'windows/amd64',
    os: 'windows',
    arch: 'amd64',
    assetName: 'lwc-sync_1.0.0_windows_amd64.zip',
    href: 'https://storage.googleapis.com/llm-wiki-frontend-config-dev/cli/versions/1.0.0/lwc-sync_1.0.0_windows_amd64.zip',
    labels: { en: 'Windows (64-bit)', 'zh-TW': 'Windows（64 位元）' },
  },
  {
    target: 'linux/amd64',
    os: 'linux',
    arch: 'amd64',
    assetName: 'lwc-sync_1.0.0_linux_amd64.tar.gz',
    href: 'https://storage.googleapis.com/llm-wiki-frontend-config-dev/cli/versions/1.0.0/lwc-sync_1.0.0_linux_amd64.tar.gz',
    labels: { en: 'Linux (64-bit)', 'zh-TW': 'Linux（64 位元）' },
  },
] as const;

const releaseManifest: CLIReleaseManifest = {
  schema_version: 1,
  version: '1.0.0',
  source_commit: 'a'.repeat(40),
  channel: 'stable',
  assets: platformAssets.map(({ os, arch, assetName, href }) => ({
    os,
    arch,
    asset_name: assetName,
    size: 1024,
    sha256: 'a'.repeat(64),
    download_url: href,
  })),
};

vi.mock('@/components/WorkspaceProvider', () => ({ useWorkspace: () => ({ currentProject: { id: 'project-123', name: 'Current Project' } }) }));
vi.mock('@/lib/runtime-config', () => ({ getRuntimeConfig: () => ({ auth_url: 'https://auth.dev.test' }) }));
vi.mock('@/lib/cli-release-metadata', () => ({ loadCLIReleaseManifest: mocks.loadManifest }));

import { CLILaneClient } from '@/components/CLILaneClient';

const { cleanup, fireEvent, render, screen } = await import('@testing-library/react');

beforeEach(() => {
  localStorage.setItem('locale', 'zh-TW');
  mocks.loadManifest.mockReset().mockResolvedValue(null);
  mocks.writeText.mockReset().mockResolvedValue(undefined);
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: mocks.writeText } });
});

afterEach(() => {
  cleanup();
  localStorage.removeItem('locale');
  vi.restoreAllMocks();
});

describe('CLI onboarding lane', () => {
  it.each([
    ['zh-TW', '用 CLI 連接你的 Wiki', '將本機 wiki 的 raw/ 資料夾同步到雲端。同步後，請回到 Web 執行編譯，完成後即可搜尋。CLI 不會讀取其他資料夾的內容。'],
    ['en', 'Connect your wiki with the CLI', 'Sync your local wiki’s raw/ folder to the cloud. After syncing, return to the web app and compile before searching. The CLI does not read content from other folders.'],
  ] as const)('shows the localized Beta heading and scope explanation in %s before the steps', (locale, title, intro) => {
    localStorage.setItem('locale', locale);
    const { container } = render(<CLILaneClient />);

    expect(screen.getByRole('heading', { level: 1, name: `${title} Beta` })).toBeTruthy();
    const introParagraph = screen.getByText(intro);
    const firstStepHeading = screen.getByRole('heading', { name: locale === 'en' ? /Download the CLI/ : /下載 CLI/ });
    expect(introParagraph.compareDocumentPosition(firstStepHeading) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(container.querySelectorAll('header h1 .ring-1')).toHaveLength(1);
  });

  it('starts with four closed cards and opens only the selected card', () => {
    render(<CLILaneClient />);
    const cards = screen.getAllByRole('button').filter((button) => button.hasAttribute('aria-expanded'));
    expect(cards).toHaveLength(4);
    expect(cards.every((button) => button.getAttribute('aria-expanded') === 'false')).toBe(true);

    fireEvent.click(cards[0]);
    expect(cards[0].getAttribute('aria-expanded')).toBe('true');
    fireEvent.click(cards[1]);
    expect(cards[0].getAttribute('aria-expanded')).toBe('false');
    expect(cards[1].getAttribute('aria-expanded')).toBe('true');
    fireEvent.click(cards[1]);
    expect(cards.every((button) => button.getAttribute('aria-expanded') === 'false')).toBe(true);
  });

  it('keeps metadata failure safe and disables root commands until a path is entered', async () => {
    render(<CLILaneClient />);
    expect(screen.getByText('具備在本機執行命令能力的人或 agent 都可依照這些步驟操作；此頁不會在你的電腦上執行命令。')).toBeTruthy();
    const cards = screen.getAllByRole('button').filter((button) => button.hasAttribute('aria-expanded'));
    fireEvent.click(cards[0]);
    expect(await screen.findAllByText('尚未提供')).toHaveLength(3);
    expect(screen.queryAllByRole('link')).toHaveLength(0);

    fireEvent.click(screen.getByRole('button', { name: 'Windows PowerShell' }));
    expect(await screen.findAllByText('尚未提供')).toHaveLength(1);
    expect(screen.queryAllByRole('link')).toHaveLength(0);

    fireEvent.click(screen.getByRole('button', { name: 'macOS / Linux' }));
    expect(await screen.findAllByText('尚未提供')).toHaveLength(3);

    fireEvent.click(cards[2]);
    expect(screen.getByText('只有在確認是同一個 Vault，且 metadata 遺失或未完成時才執行 recover。只有 binding 已撤銷且本機 host、project、wiki 與 binding identity 全部相符時才重新授權；任一不同就先停止並核對。')).toBeTruthy();
    const rootInput = screen.getByLabelText('Wiki 根目錄的絕對路徑');
    fireEvent.click(screen.getByRole('button', { name: '複製命令' }));
    expect(mocks.writeText).not.toHaveBeenCalled();
    fireEvent.change(rootInput, { target: { value: "/Users/a user's Wiki 東京" } });
    const initCommand = screen.getByText(/\.\/lwc-sync init --vault/);
    expect(initCommand.textContent).toContain("'\\''");
    fireEvent.click(screen.getByRole('button', { name: '填入 Documents 範例' }));
    expect((rootInput as HTMLInputElement).value).toBe('/Users/your-name/Documents/YourWiki');
    expect(cards[2].getAttribute('aria-expanded')).toBe('true');
  });

  it.each(['en', 'zh-TW'] as const)('filters downloads and keeps shell-specific commands in %s', async (locale) => {
    const copyLabel = locale === 'en' ? 'Copy command' : '複製命令';
    const enterRoot = locale === 'en' ? 'Enter a wiki root path to prepare this command.' : '請先輸入 Wiki 根目錄路徑，再準備這條命令。';
    const rootLabel = locale === 'en' ? 'Absolute path to your wiki root' : 'Wiki 根目錄的絕對路徑';
    const posixPlatforms = platformAssets.filter(({ target }) => target !== 'windows/amd64');
    const windowsPlatforms = platformAssets.filter(({ target }) => target === 'windows/amd64');
    const expectDownloads = async (visiblePlatforms: readonly (typeof platformAssets)[number][]) => {
      for (const platform of visiblePlatforms) {
        expect(await screen.findByRole('heading', { name: platform.labels[locale] })).toBeTruthy();
      }
      expect(screen.getAllByRole('link').map((link) => link.getAttribute('href'))).toEqual(visiblePlatforms.map(({ href }) => href));
      for (const platform of platformAssets.filter(({ target }) => !visiblePlatforms.some(({ target: visibleTarget }) => visibleTarget === target))) {
        expect(screen.queryByRole('heading', { name: platform.labels[locale] })).toBeNull();
      }
    };

    localStorage.setItem('locale', locale);
    mocks.loadManifest.mockReset().mockResolvedValue(releaseManifest);
    render(<CLILaneClient />);
    const cards = screen.getAllByRole('button').filter((button) => button.hasAttribute('aria-expanded'));
    fireEvent.click(cards[0]);
    await expectDownloads(posixPlatforms);
    expect(screen.getByText(locale === 'en' ? 'Release 1.0.0' : '發行版 1.0.0')).toBeTruthy();
    expect(screen.getByText('./lwc-sync --version')).toBeTruthy();

    fireEvent.click(cards[2]);
    const rootInput = screen.getByLabelText(rootLabel);
    fireEvent.change(rootInput, { target: { value: '/Users/example/My Wiki' } });
    expect(screen.getByText(/\.\/lwc-sync init --vault/).textContent).toContain("'/Users/example/My Wiki'");

    fireEvent.click(screen.getByRole('button', { name: 'Windows PowerShell' }));
    expect((rootInput as HTMLInputElement).value).toBe('/Users/example/My Wiki');
    expect((rootInput as HTMLInputElement).placeholder).toBe('C:\\Users\\you\\Documents\\YourWiki');
    expect(screen.getByText(enterRoot)).toBeTruthy();
    expect((screen.getByRole('button', { name: copyLabel }) as HTMLButtonElement).disabled).toBe(true);

    fireEvent.click(cards[0]);
    await expectDownloads(windowsPlatforms);
    expect(screen.getByText('.\\lwc-sync.exe --version')).toBeTruthy();

    fireEvent.click(cards[2]);
    expect((screen.getByLabelText(rootLabel) as HTMLInputElement).value).toBe('/Users/example/My Wiki');
    fireEvent.change(screen.getByLabelText(rootLabel), { target: { value: 'C:\\Users\\example\\My Wiki' } });
    expect(screen.getByText(/\.\\lwc-sync\.exe init --vault/).textContent).toContain("'C:\\Users\\example\\My Wiki'");

    fireEvent.click(cards[3]);
    const syncOption = screen.getByRole('checkbox');
    fireEvent.click(syncOption);
    expect((syncOption as HTMLInputElement).checked).toBe(true);
    expect(screen.getByText(/\.\\lwc-sync\.exe push --vault/).textContent).toContain('--sync');

    fireEvent.click(screen.getByRole('button', { name: 'macOS / Linux' }));
    expect(cards[3].getAttribute('aria-expanded')).toBe('true');
    expect((screen.getByRole('checkbox') as HTMLInputElement).checked).toBe(true);
    expect(screen.getByText(enterRoot)).toBeTruthy();
    expect((screen.getByRole('button', { name: copyLabel }) as HTMLButtonElement).disabled).toBe(true);

    fireEvent.click(cards[2]);
    const preservedRoot = screen.getByLabelText(rootLabel) as HTMLInputElement;
    expect(preservedRoot.value).toBe('C:\\Users\\example\\My Wiki');
    expect(preservedRoot.placeholder).toBe('/Users/you/Documents/YourWiki');
    fireEvent.change(preservedRoot, { target: { value: '/Users/example/My Wiki' } });
    fireEvent.click(cards[3]);
    expect((screen.getByRole('checkbox') as HTMLInputElement).checked).toBe(true);
    expect(screen.getByText(/\.\/lwc-sync push --vault/).textContent).toContain('--sync');

    fireEvent.click(cards[0]);
    await expectDownloads(posixPlatforms);
    expect(screen.getByText('./lwc-sync --version')).toBeTruthy();
  });
});
