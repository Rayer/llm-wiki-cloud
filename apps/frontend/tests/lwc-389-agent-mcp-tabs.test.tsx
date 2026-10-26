import React from 'react';
import { spawnSync } from 'node:child_process';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AgentMcpTabs } from '@/components/AgentMcpTabs';
import { buildMCPGuideSnippet, isUsableMCPResourceURL, type MCPGuideTarget } from '@/lib/mcp-client-guides';
import { translate } from '@/lib/i18n';
import type { Locale } from '@/lib/i18n-core';
import { mcpResourceURL } from '@/lib/mcp-oauth';

const { act, cleanup, fireEvent, render, screen, within } = await import('@testing-library/react');
const resourceURLA = 'https://api.example.test/mcp/projects/project%2FA%20%26%20team';
const resourceURLB = 'https://api.example.test/mcp/projects/project-B';
const guideSourcesOpenAI = 'https://help.openai.com/en/articles/12584461-connecting-custom-mcp-servers-in-chatgpt';
const scrollIntoView = vi.fn();

function renderTabs(locale: Locale = 'en', resourceURL = resourceURLA, identityKey = `user-a:project-a:${resourceURL}`) {
  const t = (key: string, params?: Record<string, string | number>) => translate(locale, key, params);
  return render(<AgentMcpTabs resourceURL={resourceURL} identityKey={identityKey} environmentLabel="Configured deployment" locale={locale} t={t} />);
}

function copyLabel(target: MCPGuideTarget) {
  if (target === 'hermes') return 'MCPOAuth.copyConfig';
  if (target === 'grok-bot' || target === 'openai-web') return 'MCPOAuth.copyPrompt';
  return 'MCPOAuth.copySetup';
}

function tabIndex(target: MCPGuideTarget) {
  if (target === 'hermes') return 0;
  if (target === 'codex' || target === 'openai-web') return 1;
  if (target === 'grok-cli' || target === 'grok-bot') return 2;
  if (target === 'claude-code') return 3;
  return 4;
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
  vi.clearAllMocks();
});

describe('LWC-389 Agent & MCP client guides', () => {
  it('builds the guide source from the configured API and encoded Project identifier', () => {
    expect(mcpResourceURL('project/a & team')).toBe('https://api.runtime.test/mcp/projects/project%2Fa%20%26%20team');
  });

  it.each(['en', 'zh-TW'] as const)('keeps the five accessible, horizontally scrollable tabs in %s', (locale) => {
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
    expect(screen.getByRole('heading', { name: t('MCPOAuth.panel.grok.cliTitle') })).toBeDefined();
    expect(screen.getByRole('heading', { name: t('MCPOAuth.panel.grok.botTitle') })).toBeDefined();
    fireEvent.keyDown(tabs[2], { key: 'Home' });
    expect(document.activeElement).toBe(tabs[0]);
    fireEvent.keyDown(tabs[0], { key: 'End' });
    expect(document.activeElement).toBe(tabs[4]);
    fireEvent.keyDown(tabs[4], { key: 'ArrowLeft' });
    expect(document.activeElement).toBe(tabs[3]);
    expect(screen.getAllByRole('tabpanel')).toHaveLength(1);
  });

  it.each([
    'hermes', 'codex', 'grok-cli', 'grok-bot', 'claude-code', 'openclaw',
  ] as MCPGuideTarget[])('shows and copies the selected Project setup for %s, then invalidates A feedback on B', async (target) => {
    const t = (key: string) => translate('en', key);
    const { rerender } = renderTabs();
    const tabs = screen.getAllByRole('tab');
    if (tabIndex(target) !== 0) fireEvent.click(tabs[tabIndex(target)]);

    const panelA = screen.getByRole('tabpanel');
    const snippetA = buildMCPGuideSnippet(target, resourceURLA);
    expect(within(panelA).getByText((_, element) => element?.tagName === 'CODE' && element.textContent === snippetA)).toBeDefined();
    expect(snippetA).toContain(resourceURLA);
    expect(snippetA).not.toContain('example.invalid');
    expect(panelA.textContent).toContain('NOT RUN');

    const writeText = navigator.clipboard.writeText as ReturnType<typeof vi.fn>;
    fireEvent.click(within(panelA).getByRole('button', { name: t(copyLabel(target)) }));
    expect(writeText).toHaveBeenLastCalledWith(snippetA);
    expect(await within(panelA).findByRole('button', { name: t('MCPOAuth.copied') })).toBeDefined();

    rerender(<AgentMcpTabs resourceURL={resourceURLB} identityKey={`user-a:project-b:${resourceURLB}`} environmentLabel="Configured deployment" t={t} />);
    const panelB = screen.getByRole('tabpanel');
    const snippetB = buildMCPGuideSnippet(target, resourceURLB);
    const codeB = within(panelB).getByText((_, element) => element?.tagName === 'CODE' && element.textContent === snippetB);
    expect(codeB.textContent).toContain(resourceURLB);
    expect(codeB.textContent).not.toContain(resourceURLA);
    expect(within(panelB).queryByRole('button', { name: t('MCPOAuth.copied') })).toBeNull();
    expect(within(panelB).getByRole('button', { name: t(copyLabel(target)) })).toBeDefined();
    expect(panelB.textContent).not.toContain('example.invalid');
  });

  it('does not surface a late clipboard completion after the Project changes', async () => {
    let resolveCopy!: () => void;
    const pendingCopy = new Promise<void>((resolve) => { resolveCopy = resolve; });
    const writeText = vi.fn().mockReturnValue(pendingCopy);
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
    const t = (key: string) => translate('en', key);
    const { rerender } = renderTabs();
    fireEvent.click(screen.getByRole('tab', { name: t('MCPOAuth.toolCodex') }));
    const panelA = screen.getByRole('tabpanel');
    fireEvent.click(within(panelA).getByRole('button', { name: t('MCPOAuth.copySetup') }));
    expect(writeText).toHaveBeenCalledWith(buildMCPGuideSnippet('codex', resourceURLA));

    rerender(<AgentMcpTabs resourceURL={resourceURLB} identityKey={`user-a:project-b:${resourceURLB}`} environmentLabel="Configured deployment" t={t} />);
    await act(async () => {
      resolveCopy();
      await pendingCopy;
    });
    expect(screen.queryByRole('button', { name: t('MCPOAuth.copied') })).toBeNull();
    expect(screen.getByText((_, element) => element?.tagName === 'CODE' && element.textContent === buildMCPGuideSnippet('codex', resourceURLB))).toBeDefined();
  });

  it('quotes shell and JSON arguments, rejects invalid resources, and keeps Grok CLI separate from Bot URL', () => {
    const urlWithQuote = "https://api.example.test/mcp/projects/a';printf INJECTED >&2;printf '";
    const codex = buildMCPGuideSnippet('codex', urlWithQuote);
    const openclaw = buildMCPGuideSnippet('openclaw', urlWithQuote);
    expect(codex).toContain("'\\''");
    expect(openclaw).toContain("'\\''");
    expect(openclaw).toContain('"transport":"streamable-http"');
    expect(openclaw).toContain('"scope":"mcp:query mcp:raw:write"');
    const shellArg = (command: string, prefix: string) => {
      const argument = command.split('\n')[0].slice(prefix.length);
      return spawnSync('/bin/sh', ['-c', `set -- ${argument}; printf '%s' "$1"`], { encoding: 'utf8' });
    };
    const codexRoundTrip = shellArg(codex, 'codex mcp add llm-wiki --url ');
    expect(codexRoundTrip.status).toBe(0);
    expect(codexRoundTrip.stderr).toBe('');
    expect(codexRoundTrip.stdout).toBe(urlWithQuote);
    const openclawRoundTrip = shellArg(openclaw, 'openclaw mcp set llm-wiki ');
    expect(openclawRoundTrip.status).toBe(0);
    expect(openclawRoundTrip.stderr).toBe('');
    expect(JSON.parse(openclawRoundTrip.stdout)).toEqual({
      url: urlWithQuote,
      transport: 'streamable-http',
      auth: 'oauth',
      oauth: { scope: 'mcp:query mcp:raw:write' },
    });
    expect(buildMCPGuideSnippet('grok-cli', resourceURLA)).toMatch(/^grok mcp add --transport http llm-wiki /);
    expect(buildMCPGuideSnippet('grok-bot', resourceURLA, 'en')).toContain(resourceURLA);
    expect(isUsableMCPResourceURL(resourceURLA)).toBe(true);
    for (const value of ['', 'not a URL', 'ftp://api.example.test/mcp', 'https://user:secret@api.example.test/mcp', 'https://api.example.test/mcp#fragment']) {
      expect(isUsableMCPResourceURL(value)).toBe(false);
      expect(buildMCPGuideSnippet('codex', value)).toBe('');
    }

    renderTabs('en', 'ftp://api.example.test/mcp');
    expect(screen.getAllByText(translate('en', 'MCPOAuth.resourceUnavailable')).length).toBeGreaterThan(0);
    expect((screen.getByRole('button', { name: translate('en', 'MCPOAuth.copyConfig') }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('labels the cloud Bot and documentation-only clients without claiming an LWC pass', () => {
    renderTabs('zh-TW');
    const tabs = screen.getAllByRole('tab');
    fireEvent.click(tabs[2]);
    const grokPanel = screen.getByRole('tabpanel');
    expect(grokPanel.textContent).toContain('Grok CLI / Grok Build（本機）');
    expect(grokPanel.textContent).toContain('xAI Grok Bot（雲端，獨立設定）');
    expect(grokPanel.textContent).toContain('尚未執行');
    expect(document.body.textContent).not.toContain('即將');
    expect(document.body.textContent).not.toContain('Bearer');
    expect(document.body.textContent).not.toContain('example.invalid');
  });

  it.each(['zh-TW', 'en'] as const)('shows and copies the localized Grok Bot request for the current Project in %s', async (locale) => {
    const t = (key: string) => translate(locale, key);
    renderTabs(locale);
    fireEvent.click(screen.getByRole('tab', { name: t('MCPOAuth.toolGrok') }));
    const panel = screen.getByRole('tabpanel');
    const prompt = buildMCPGuideSnippet('grok-bot', resourceURLA, locale);
    expect(within(panel).getByText((_, element) => element?.tagName === 'CODE' && element.textContent === prompt)).toBeDefined();
    expect(prompt).toContain(resourceURLA);
    expect(prompt).toContain(locale === 'zh-TW' ? '請填寫此 Project 要處理的資料用途' : '[describe the data this Project should handle]');
    expect(prompt).not.toContain('Setup → Plugins');
    fireEvent.click(within(panel).getByRole('button', { name: t('MCPOAuth.copyPrompt') }));
    expect(navigator.clipboard.writeText).toHaveBeenLastCalledWith(prompt);
    expect(await within(panel).findByRole('button', { name: t('MCPOAuth.copied') })).toBeDefined();
  });

  it.each(['zh-TW', 'en'] as const)('keeps Codex and adds the shared OpenAI web prompt in the existing OpenAI tab for %s', async (locale) => {
    const t = (key: string) => translate(locale, key);
    renderTabs(locale);
    const openaiTab = screen.getByRole('tab', { name: 'OpenAI' });
    expect(openaiTab).toBeDefined();
    fireEvent.click(openaiTab);
    const panel = screen.getByRole('tabpanel');
    expect(within(panel).getByRole('heading', { name: t('MCPOAuth.panel.codex.title') })).toBeDefined();
    expect(within(panel).getByRole('heading', { name: t('MCPOAuth.panel.openaiWeb.title') })).toBeDefined();
    const prompt = buildMCPGuideSnippet('openai-web', resourceURLA, locale);
    expect(within(panel).getByText((_, element) => element?.tagName === 'CODE' && element.textContent === prompt)).toBeDefined();
    expect(prompt).toContain(resourceURLA);
    expect(prompt).not.toContain('example.invalid');
    const guideLinks = within(panel).getAllByRole('link', { name: t('MCPOAuth.officialGuide') });
    expect(guideLinks.some((link) => link.getAttribute('href') === guideSourcesOpenAI)).toBe(true);
    fireEvent.click(within(panel).getByRole('button', { name: t('MCPOAuth.copyPrompt') }));
    expect(navigator.clipboard.writeText).toHaveBeenLastCalledWith(prompt);
    expect(await within(panel).findByRole('button', { name: t('MCPOAuth.copied') })).toBeDefined();
    expect(panel.textContent).toContain(locale === 'zh-TW' ? 'Dot 共用流程由 Owner 確認' : 'The shared Dot flow is Owner-confirmed');
    expect(panel.textContent).toContain(locale === 'zh-TW' ? '尚未執行' : 'NOT RUN');
  });

  it('invalidates a pending connection-prompt copy when the locale changes', async () => {
    let resolveCopy!: () => void;
    const pendingCopy = new Promise<void>((resolve) => { resolveCopy = resolve; });
    const writeText = vi.fn().mockReturnValue(pendingCopy);
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
    const tEn = (key: string) => translate('en', key);
    const tZh = (key: string) => translate('zh-TW', key);
    const { rerender } = render(
      <AgentMcpTabs
        resourceURL={resourceURLA}
        identityKey={'user-a:project-a:' + resourceURLA + ':en'}
        environmentLabel="Configured deployment"
        locale="en"
        t={tEn}
      />,
    );
    fireEvent.click(screen.getByRole('tab', { name: 'OpenAI' }));
    const englishPanel = screen.getByRole('tabpanel');
    const englishPrompt = buildMCPGuideSnippet('openai-web', resourceURLA, 'en');
    fireEvent.click(within(englishPanel).getByRole('button', { name: tEn('MCPOAuth.copyPrompt') }));
    expect(writeText).toHaveBeenLastCalledWith(englishPrompt);

    rerender(
      <AgentMcpTabs
        resourceURL={resourceURLA}
        identityKey={'user-a:project-a:' + resourceURLA + ':zh-TW'}
        environmentLabel="Configured deployment"
        locale="zh-TW"
        t={tZh}
      />,
    );
    await act(async () => {
      resolveCopy();
      await pendingCopy;
    });
    const chinesePanel = screen.getByRole('tabpanel');
    expect(screen.queryByRole('button', { name: tZh('MCPOAuth.copied') })).toBeNull();
    expect(within(chinesePanel).getByText((_, element) => element?.tagName === 'CODE'
      && element.textContent === buildMCPGuideSnippet('openai-web', resourceURLA, 'zh-TW'))).toBeDefined();
  });
});
