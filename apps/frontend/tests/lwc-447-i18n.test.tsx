import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { translate, localeHasFallback, catalogRevision } from '@/lib/i18n';
import type { Locale } from '@/lib/i18n-core';
import { LocaleControl } from '@/components/LocaleControl';
import { AgentMcpTabs } from '@/components/AgentMcpTabs';

const { act, cleanup, fireEvent, render, screen } = await import('@testing-library/react');
const resourceURL = 'https://api.example.test/mcp/projects/project-a';
const revision = catalogRevision;
const committedProviderKeys = [
  ...['discoveryPartial', 'discoveryStale', 'operationDiscoveryUnsupported', 'operationModelNotFound', 'operationParameterUnsupported', 'operationPermissionError'].map((key) => `BYOK.${key}`),
  ...['discoveryPartial', 'discoveryStale', 'operationDiscoveryUnsupported', 'operationModelNotFound', 'operationParameterUnsupported', 'operationPermissionError'].map((key) => `Admin.sharedProfiles.${key}`),
];

function renderAgent(locale: Locale, identity = `project-a:${locale}:${revision}`) {
  const t = (key: string, params?: Record<string, string | number>) => translate(locale, key, params);
  return render(<AgentMcpTabs resourceURL={resourceURL} environmentLabel="Configured" identityKey={identity} locale={locale} t={t} />);
}

beforeEach(() => {
  localStorage.clear();
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText: vi.fn().mockResolvedValue(undefined) },
  });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: undefined });
});

describe('LWC-447 runtime catalogs and locale UI', () => {
  it('uses the prepared Traditional catalog and exposes fallback status for Simplified Chinese', () => {
    expect(revision).toMatch(/^[a-f0-9]{64}$/);
    expect(translate('zh-TW', 'Login.title')).toBe('登入');
    expect(translate('en', 'Login.title')).toBe('Sign In');
    expect(translate('zh-CN', 'Login.title')).toBe('Sign In');
    expect(localeHasFallback('zh-TW')).toBe(false);
    expect(localeHasFallback('zh-CN')).toBe(true);
    expect(localeHasFallback('en')).toBe(false);
    expect(translate('zh-TW', 'MCPOAuth.sharedInstructions')).toContain('不代表已獲授權');
    expect(translate('zh-TW', 'MCPOAuth.sharedInstructions')).not.toContain('@cac:');
  });

  it('keeps committed BYOK and shared-profile copy in the catalog with explicit Simplified fallback', () => {
    for (const key of committedProviderKeys) {
      const params = key.endsWith('.discoveryPartial') ? { count: 4 } : undefined;
      const english = translate('en', key, params);
      const traditional = translate('zh-TW', key, params);
      expect(english).not.toBe(key);
      expect(traditional).not.toBe(key);
      expect(traditional).not.toBe(english);
      expect(translate('zh-CN', key, params)).toBe(english);
    }
    expect(translate('en', 'BYOK.discoveryPartial', { count: 4 })).toContain('4 models');
    expect(translate('zh-TW', 'Admin.sharedProfiles.discoveryPartial', { count: 4 })).toContain('4 個');
  });

  it('persists locale selection, follows other tabs, and offers a direct English switch on fallback', () => {
    render(<LocaleControl />);
    const select = screen.getByRole('combobox', { name: translate('zh-TW', 'Shell.language') }) as HTMLSelectElement;
    expect(select.value).toBe('zh-TW');
    fireEvent.change(select, { target: { value: 'zh-CN' } });
    expect(localStorage.getItem('locale')).toBe('zh-CN');
    expect(screen.getByRole('status').textContent).toContain(translate('en', 'Shell.translationFallback'));
    fireEvent.click(screen.getByRole('button', { name: translate('en', 'Shell.useEnglish') }));
    expect(select.value).toBe('en');
    expect(localStorage.getItem('locale')).toBe('en');
    expect(screen.queryByRole('status')).toBeNull();

    localStorage.setItem('locale', 'zh_tw');
    act(() => window.dispatchEvent(new StorageEvent('storage', { key: 'locale' })));
    expect(select.value).toBe('zh-TW');
  });

  it('renders and copies exactly the selected-locale CaC shared instructions, and fences stale locale copies', async () => {
    const traditional = translate('zh-TW', 'MCPOAuth.sharedInstructions');
    const { rerender, container } = renderAgent('zh-TW');
    fireEvent.click(screen.getByText(translate('zh-TW', 'MCPOAuth.sharedInstructionsTitle')));
    const paragraph = screen.getByText((_, element) => element?.tagName === 'P' && element.textContent === traditional);
    expect(paragraph.textContent).toBe(traditional);
    fireEvent.click(screen.getByRole('button', { name: translate('zh-TW', 'MCPOAuth.copySharedInstructions') }));
    expect(navigator.clipboard.writeText).toHaveBeenLastCalledWith(traditional);
    expect(await screen.findByRole('button', { name: translate('zh-TW', 'MCPOAuth.sharedInstructionsCopied') })).toBeDefined();

    let finishCopy!: () => void;
    const pending = new Promise<void>((resolve) => { finishCopy = resolve; });
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: vi.fn(() => pending) } });
    rerender(<AgentMcpTabs
      resourceURL={resourceURL}
      environmentLabel="Configured"
      identityKey={`project-a:zh-TW:${revision}:second-copy`}
      locale="zh-TW"
      t={(key, params) => translate('zh-TW', key, params)}
    />);
    fireEvent.click(screen.getByRole('button', { name: translate('zh-TW', 'MCPOAuth.copySharedInstructions') }));
    rerender(<AgentMcpTabs
      resourceURL={resourceURL}
      environmentLabel="Configured"
      identityKey={`project-a:en:${revision}`}
      locale="en"
      t={(key, params) => translate('en', key, params)}
    />);
    await act(async () => { finishCopy(); await pending; });
    const sharedInstructions = container.querySelector('section > div > details');
    expect(sharedInstructions?.querySelector('p')?.textContent).toBe(translate('en', 'MCPOAuth.sharedInstructions'));
    expect(sharedInstructions?.querySelector('button')?.textContent).toBe(translate('en', 'MCPOAuth.copySharedInstructions'));
  });
});
