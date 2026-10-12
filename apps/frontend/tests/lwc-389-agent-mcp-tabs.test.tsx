import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AgentMcpTabs } from '@/components/AgentMcpTabs';
import { translate } from '@/lib/i18n';
import type { Locale } from '@/lib/i18n-core';

const { cleanup, fireEvent, render, screen, within } = await import('@testing-library/react');
const resourceURL = 'http://localhost:18080/mcp/projects/current-project';
const configSnippet = [
  'mcp_servers:',
  '  llm_wiki:',
  `    url: "${resourceURL}"`,
  '    auth: oauth',
  '    oauth:',
  '      cimd: false',
  '      scope: mcp:query',
].join('\n');
const scrollIntoView = vi.fn();

function renderTabs(locale: Locale = 'en') {
  const t = (key: string, params?: Record<string, string | number>) => translate(locale, key, params);
  return render(<AgentMcpTabs resourceURL={resourceURL} configSnippet={configSnippet} environmentLabel="Local development" t={t} />);
}

beforeEach(() => {
  scrollIntoView.mockReset();
  Object.defineProperty(HTMLElement.prototype, 'scrollIntoView', { configurable: true, value: scrollIntoView });
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: vi.fn().mockResolvedValue(undefined) } });
});

afterEach(() => {
  cleanup();
  Reflect.deleteProperty(HTMLElement.prototype, 'scrollIntoView');
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: undefined });
});

describe('LWC-389 Agent & MCP tabs', () => {
  it.each(['en', 'zh-TW'] as const)('supports the five tools, roving keyboard focus, and selected-tab scrolling in %s', (locale) => {
    const t = (key: string) => translate(locale, key);
    renderTabs(locale);
    const tabList = screen.getByRole('tablist', { name: t('MCPOAuth.toolsTabList') });
    expect(tabList.className).toContain('overflow-x-auto');
    const tabs = screen.getAllByRole('tab');
    expect(tabs.map((tab) => tab.textContent)).toEqual([
      t('MCPOAuth.toolHermes'), t('MCPOAuth.toolCodex'), t('MCPOAuth.toolGrok'),
      t('MCPOAuth.toolClaudeCode'), t('MCPOAuth.toolOpenClaw'),
    ]);
    expect(tabs.map((tab) => (tab as HTMLButtonElement).tabIndex)).toEqual([0, -1, -1, -1, -1]);
    expect(screen.getAllByRole('tabpanel')).toHaveLength(1);
    expect(screen.getByRole('heading', { name: t('MCPOAuth.hermesPanelTitle') })).toBeDefined();

    fireEvent.keyDown(tabs[0], { key: 'ArrowRight' });
    expect(document.activeElement).toBe(tabs[1]);
    expect(tabs[1].getAttribute('aria-selected')).toBe('true');
    expect(scrollIntoView).toHaveBeenCalledWith({ block: 'nearest', inline: 'nearest' });
    fireEvent.keyDown(tabs[1], { key: 'ArrowRight' });
    expect(screen.getByRole('heading', { name: t('MCPOAuth.panel.grok.title') })).toBeDefined();
    expect(screen.getByText(t('MCPOAuth.panel.grok.body'))).toBeDefined();
    fireEvent.keyDown(tabs[2], { key: 'Home' });
    expect(document.activeElement).toBe(tabs[0]);
    fireEvent.keyDown(tabs[0], { key: 'End' });
    expect(document.activeElement).toBe(tabs[4]);
    fireEvent.keyDown(tabs[4], { key: 'ArrowLeft' });
    expect(document.activeElement).toBe(tabs[3]);
    expect(screen.getAllByRole('tabpanel')).toHaveLength(1);
  });

  it('shows only the real project resource configuration for Hermes and copies that exact text', async () => {
    renderTabs();
    const visibleConfig = screen.getByText((_, element) => element?.tagName === 'CODE') as HTMLElement;
    expect(visibleConfig.textContent).toBe(configSnippet);
    expect(visibleConfig.textContent).toContain(resourceURL);
    expect(visibleConfig.textContent).toContain('scope: mcp:query');
    expect(visibleConfig.textContent).not.toContain('Bearer');
    const writeText = navigator.clipboard.writeText as ReturnType<typeof vi.fn>;
    fireEvent.click(screen.getByRole('button', { name: translate('en', 'MCPOAuth.copyConfig') }));
    expect(writeText).toHaveBeenCalledWith(configSnippet);
    expect(await screen.findByRole('button', { name: translate('en', 'MCPOAuth.copied') })).toBeDefined();
  });

  it('uses honest neutral placeholders and distinguishes Grok CLI from web', () => {
    renderTabs('zh-TW');
    const tabs = screen.getAllByRole('tab');
    for (const index of [1, 2, 3, 4]) {
      fireEvent.click(tabs[index]);
      const selectedPanel = within(screen.getByRole('tabpanel'));
      expect(selectedPanel.getAllByText('設定指引準備中')).toHaveLength(2);
      if (index === 2) {
        expect(selectedPanel.getByText('Grok CLI 與 Grok 網頁版可能使用不同連線方式；此處尚未驗證任一方式。')).toBeDefined();
      } else expect(selectedPanel.getByText('尚未驗證此用戶端的設定步驟。')).toBeDefined();
      expect(selectedPanel.getByText('此處為佔位內容，不代表此用戶端已測試或支援 OAuth。')).toBeDefined();
    }
    expect(document.body.textContent).not.toContain('即將');
    expect(document.body.textContent).not.toContain('Bearer');
  });
});
