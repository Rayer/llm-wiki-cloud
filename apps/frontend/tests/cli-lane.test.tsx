import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({ loadManifest: vi.fn(), writeText: vi.fn() }));

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
    expect(await screen.findAllByText('尚未提供')).toHaveLength(4);
    expect(screen.queryAllByRole('link')).toHaveLength(0);

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

  it.each([
    ['en', ['macOS (Apple Silicon)', 'macOS (Intel)', 'Windows (64-bit)', 'Linux (64-bit)']],
    ['zh-TW', ['macOS（Apple Silicon）', 'macOS（Intel）', 'Windows（64 位元）', 'Linux（64 位元）']],
  ])('translates every supported platform label in %s', async (locale, labels) => {
    localStorage.setItem('locale', locale);
    render(<CLILaneClient />);
    const cards = screen.getAllByRole('button').filter((button) => button.hasAttribute('aria-expanded'));
    fireEvent.click(cards[0]);

    for (const label of labels) {
      expect(await screen.findByRole('heading', { name: label })).toBeTruthy();
    }
  });
});
