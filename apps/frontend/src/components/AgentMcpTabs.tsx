'use client';

import { useEffect, useRef, useState } from 'react';
import type { Locale } from '@/lib/i18n-core';
import { buildMCPGuideSnippet, isUsableMCPResourceURL, type MCPGuideTarget } from '@/lib/mcp-client-guides';

type Translate = (key: string, params?: Record<string, string | number>) => string;
type ToolId = 'hermes' | 'codex' | 'grok' | 'claude-code' | 'openclaw';
type CopyFeedback = { identityKey: string; target: MCPGuideTarget; status: 'copied' | 'error' };

const tools: { id: ToolId; labelKey: string }[] = [
  { id: 'hermes', labelKey: 'MCPOAuth.toolHermes' },
  { id: 'codex', labelKey: 'MCPOAuth.toolCodex' },
  { id: 'grok', labelKey: 'MCPOAuth.toolGrok' },
  { id: 'claude-code', labelKey: 'MCPOAuth.toolClaudeCode' },
  { id: 'openclaw', labelKey: 'MCPOAuth.toolOpenClaw' },
];

const guideSources: Partial<Record<MCPGuideTarget, string>> = {
  codex: 'https://developers.openai.com/codex/extend/mcp',
  'openai-web': 'https://help.openai.com/en/articles/12584461-connecting-custom-mcp-servers-in-chatgpt',
  'grok-cli': 'https://docs.x.ai/build/features/mcp-servers',
  'grok-bot': 'https://docs.x.ai/grok-bot/team-bots',
  'claude-code': 'https://code.claude.com/docs/en/mcp-configuration',
  openclaw: 'https://docs.openclaw.ai/tools/mcp',
};

function tabId(id: ToolId) { return `agent-mcp-tab-${id}`; }
function panelId(id: ToolId) { return `agent-mcp-panel-${id}`; }

export function AgentMcpTabs({
  resourceURL,
  environmentLabel,
  identityKey,
  locale = 'en',
  t,
}: {
  resourceURL: string;
  environmentLabel: string;
  identityKey: string;
  locale?: Locale;
  t: Translate;
}) {
  const [selectedTool, setSelectedTool] = useState<ToolId>('hermes');
  const [copyFeedback, setCopyFeedback] = useState<CopyFeedback | null>(null);
  const tabRefs = useRef<Array<HTMLButtonElement | null>>([]);
  const identityRef = useRef({ key: identityKey, generation: 0 });
  const copyOperationRef = useRef(0);
  const copyTimeoutRef = useRef<number | null>(null);

  useEffect(() => {
    if (identityRef.current.key === identityKey) return;
    identityRef.current = { key: identityKey, generation: identityRef.current.generation + 1 };
    copyOperationRef.current += 1;
    setCopyFeedback(null);
    if (copyTimeoutRef.current !== null) window.clearTimeout(copyTimeoutRef.current);
    copyTimeoutRef.current = null;
  }, [identityKey]);

  useEffect(() => () => {
    copyOperationRef.current += 1;
    if (copyTimeoutRef.current !== null) window.clearTimeout(copyTimeoutRef.current);
  }, []);

  const selectTool = (index: number, focus: boolean) => {
    const tool = tools[index];
    if (!tool) return;
    setSelectedTool(tool.id);
    if (focus) tabRefs.current[index]?.focus();
    tabRefs.current[index]?.scrollIntoView?.({ block: 'nearest', inline: 'nearest' });
  };

  const handleKeyDown = (event: React.KeyboardEvent<HTMLButtonElement>, index: number) => {
    let nextIndex: number | undefined;
    if (event.key === 'ArrowRight') nextIndex = (index + 1) % tools.length;
    else if (event.key === 'ArrowLeft') nextIndex = (index + tools.length - 1) % tools.length;
    else if (event.key === 'Home') nextIndex = 0;
    else if (event.key === 'End') nextIndex = tools.length - 1;
    if (nextIndex === undefined) return;
    event.preventDefault();
    selectTool(nextIndex, true);
  };

  const copySnippet = async (target: MCPGuideTarget) => {
    const snippet = buildMCPGuideSnippet(target, resourceURL, locale);
    const identity = identityRef.current;
    if (!snippet || identity.key !== identityKey) return;
    const operation = ++copyOperationRef.current;
    setCopyFeedback(null);
    if (copyTimeoutRef.current !== null) window.clearTimeout(copyTimeoutRef.current);
    copyTimeoutRef.current = null;
    try {
      await navigator.clipboard.writeText(snippet);
      if (identityRef.current.key !== identityKey || identityRef.current.generation !== identity.generation
        || copyOperationRef.current !== operation) return;
      setCopyFeedback({ identityKey, target, status: 'copied' });
      copyTimeoutRef.current = window.setTimeout(() => {
        if (identityRef.current.key === identityKey && identityRef.current.generation === identity.generation
          && copyOperationRef.current === operation) setCopyFeedback(null);
        copyTimeoutRef.current = null;
      }, 2000);
    } catch {
      if (identityRef.current.key === identityKey && identityRef.current.generation === identity.generation
        && copyOperationRef.current === operation) {
        setCopyFeedback({ identityKey, target, status: 'error' });
      }
    }
  };

  const renderCopyBlock = (target: MCPGuideTarget, buttonKey: string) => {
    const snippet = buildMCPGuideSnippet(target, resourceURL, locale);
    const feedback = copyFeedback?.identityKey === identityKey
      && copyFeedback.target === target
      && selectedTool === (target === 'grok-cli' || target === 'grok-bot' ? 'grok' : target === 'openai-web' ? 'codex' : target)
      ? copyFeedback.status
      : null;
    return (
      <div className="mt-4">
        <pre className="overflow-x-auto rounded-lg border border-white/10 bg-black/30 p-4 text-xs leading-5 text-zinc-200"><code>{snippet || t('MCPOAuth.resourceUnavailable')}</code></pre>
        <div className="mt-3 flex flex-wrap items-center gap-3">
          <button type="button" disabled={!snippet} onClick={() => void copySnippet(target)} className="min-h-11 rounded-lg bg-emerald-300 px-4 py-2 text-sm font-semibold text-black hover:bg-emerald-200 disabled:cursor-not-allowed disabled:opacity-50">{feedback === 'copied' ? t('MCPOAuth.copied') : t(buttonKey)}</button>
          <span role="status" className="text-sm text-zinc-400">{feedback === 'error' ? t('MCPOAuth.copyFailed') : ''}</span>
        </div>
      </div>
    );
  };

  const renderSource = (target: MCPGuideTarget) => {
    const href = guideSources[target];
    if (!href) return null;
    return <a className="mt-3 inline-block text-sm text-emerald-200 underline decoration-emerald-200/40 underline-offset-4 hover:text-white" href={href} target="_blank" rel="noreferrer">{t('MCPOAuth.officialGuide')}</a>;
  };

  const renderTechnicalDetails = (scope: string) => (
    <details className="mt-4 rounded-lg border border-white/8 bg-black/15 px-4 py-3">
      <summary className="cursor-pointer text-sm text-zinc-300 focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-300">{t('MCPOAuth.technicalDetails')}</summary>
      <dl className="mt-3 grid gap-3 text-sm sm:grid-cols-2">
        <div><dt className="text-xs text-zinc-500">{t('MCPOAuth.environment')}</dt><dd className="mt-1 text-zinc-200">{environmentLabel}</dd></div>
        <div><dt className="text-xs text-zinc-500">{t('MCPOAuth.scope')}</dt><dd className="mt-1 font-mono text-emerald-200">{scope}</dd></div>
        <div className="sm:col-span-2"><dt className="text-xs text-zinc-500">{t('MCPOAuth.resource')}</dt><dd className="mt-1 break-all font-mono text-xs leading-5 text-zinc-300">{isUsableMCPResourceURL(resourceURL) ? resourceURL : t('MCPOAuth.resourceUnavailable')}</dd></div>
      </dl>
    </details>
  );

  const renderSingleGuide = (id: Exclude<ToolId, 'hermes' | 'grok'>, target: Extract<MCPGuideTarget, 'codex' | 'claude-code' | 'openclaw'>) => (
    <div>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <h3 className="text-lg font-semibold text-white">{t(`MCPOAuth.panel.${id}.title`)}</h3>
        <span className="rounded-md border border-amber-300/20 bg-amber-300/5 px-2.5 py-1 text-xs text-amber-100">{t('MCPOAuth.productNotVerified')}</span>
      </div>
      <p className="mt-3 text-sm leading-6 text-zinc-300">{t(`MCPOAuth.panel.${id}.body`)}</p>
      {renderCopyBlock(target, 'MCPOAuth.copySetup')}
      <p className="mt-3 text-xs leading-5 text-amber-100">{t(`MCPOAuth.panel.${id}.evidence`)}</p>
      {renderSource(target)}
      {renderTechnicalDetails(target === 'claude-code' ? 'mcp:query' : 'mcp:query mcp:raw:write')}
    </div>
  );

  return (
    <section className="overflow-hidden rounded-2xl border border-white/10 bg-[#151515]" aria-labelledby="mcp-oauth-setup-title">
      <div className="p-5 pb-0 sm:p-6 sm:pb-0">
        <p className="text-xs font-semibold uppercase tracking-[0.18em] text-emerald-300">{t('MCPOAuth.setupEyebrow')}</p>
        <h2 id="mcp-oauth-setup-title" className="mt-2 text-lg font-semibold text-white">{t('MCPOAuth.setupTitle')}</h2>
        <p className="mt-2 text-sm leading-6 text-zinc-400">{t('MCPOAuth.setupIntro')}</p>
      </div>

      <div role="tablist" aria-label={t('MCPOAuth.toolsTabList')} aria-orientation="horizontal" className="mt-4 flex max-w-full snap-x snap-mandatory overflow-x-auto border-b border-white/10 px-3 [scrollbar-width:thin] sm:px-4">
        {tools.map((tool, index) => (
          <button
            key={tool.id}
            ref={(element) => { tabRefs.current[index] = element; }}
            id={tabId(tool.id)}
            type="button"
            role="tab"
            aria-selected={selectedTool === tool.id}
            aria-controls={panelId(tool.id)}
            tabIndex={selectedTool === tool.id ? 0 : -1}
            onClick={() => selectTool(index, false)}
            onKeyDown={(event) => handleKeyDown(event, index)}
            className={`min-h-12 shrink-0 snap-start border-b-2 px-4 text-sm font-medium focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-[-2px] focus-visible:outline-emerald-300 ${selectedTool === tool.id ? 'border-emerald-300 bg-emerald-300/5 text-emerald-200' : 'border-transparent text-zinc-400 hover:text-white'}`}
          >
            {t(tool.labelKey)}
          </button>
        ))}
      </div>

      {tools.map((tool) => (
        <div
          key={tool.id}
          role="tabpanel"
          id={panelId(tool.id)}
          aria-labelledby={tabId(tool.id)}
          tabIndex={0}
          hidden={selectedTool !== tool.id}
          className="p-5 focus-visible:outline focus-visible:outline-2 focus-visible:outline-inset focus-visible:outline-emerald-300 sm:p-6"
        >
          {tool.id === 'hermes' ? (
            <div>
              <div className="flex flex-wrap items-start justify-between gap-3">
                <h3 className="text-lg font-semibold text-white">{t('MCPOAuth.hermesPanelTitle')}</h3>
                <span className="rounded-md border border-amber-300/20 bg-amber-300/5 px-2.5 py-1 text-xs text-amber-100">{t('MCPOAuth.productNotVerified')}</span>
              </div>
              <ol className="mt-4 list-decimal space-y-2 pl-5 text-sm leading-6 text-zinc-300">
                <li>{t('MCPOAuth.stepOne')}</li>
                <li>{t('MCPOAuth.stepTwo')}</li>
                <li>{t('MCPOAuth.stepThree')}</li>
              </ol>
              {renderCopyBlock('hermes', 'MCPOAuth.copyConfig')}
              <p className="mt-3 text-xs leading-5 text-amber-100">{t('MCPOAuth.nativeCheck')}</p>
              {renderTechnicalDetails('mcp:query mcp:raw:write')}
            </div>
          ) : tool.id === 'grok' ? (
            <div>
              <div className="flex flex-wrap items-start justify-between gap-3">
                <h3 className="text-lg font-semibold text-white">{t('MCPOAuth.panel.grok.title')}</h3>
                <span className="rounded-md border border-amber-300/20 bg-amber-300/5 px-2.5 py-1 text-xs text-amber-100">{t('MCPOAuth.productNotVerified')}</span>
              </div>
              <p className="mt-3 text-sm leading-6 text-zinc-300">{t('MCPOAuth.panel.grok.body')}</p>
              <section className="mt-5 rounded-xl border border-white/10 bg-black/15 p-4 sm:p-5" aria-labelledby="grok-cli-guide-title">
                <h4 id="grok-cli-guide-title" className="font-semibold text-white">{t('MCPOAuth.panel.grok.cliTitle')}</h4>
                <p className="mt-2 text-sm leading-6 text-zinc-300">{t('MCPOAuth.panel.grok.cliBody')}</p>
                {renderCopyBlock('grok-cli', 'MCPOAuth.copySetup')}
                <p className="mt-3 text-xs leading-5 text-amber-100">{t('MCPOAuth.panel.grok.cliEvidence')}</p>
                {renderSource('grok-cli')}
              </section>
              <section className="mt-4 rounded-xl border border-white/10 bg-black/15 p-4 sm:p-5" aria-labelledby="grok-bot-guide-title">
                <h4 id="grok-bot-guide-title" className="font-semibold text-white">{t('MCPOAuth.panel.grok.botTitle')}</h4>
                <p className="mt-2 text-sm leading-6 text-zinc-300">{t('MCPOAuth.panel.grok.botBody')}</p>
                {renderCopyBlock('grok-bot', 'MCPOAuth.copyPrompt')}
                <p className="mt-3 text-xs leading-5 text-amber-100">{t('MCPOAuth.panel.grok.botEvidence')}</p>
                {renderSource('grok-bot')}
              </section>
              {renderTechnicalDetails('mcp:query')}
            </div>
          ) : tool.id === 'codex' ? (
            <>
              {renderSingleGuide('codex', 'codex')}
              <section className="mt-6 rounded-xl border border-white/10 bg-black/15 p-4 sm:p-5" aria-labelledby="openai-web-guide-title">
                <div className="flex flex-wrap items-start justify-between gap-3">
                  <h3 id="openai-web-guide-title" className="text-lg font-semibold text-white">{t('MCPOAuth.panel.openaiWeb.title')}</h3>
                  <span className="rounded-md border border-amber-300/20 bg-amber-300/5 px-2.5 py-1 text-xs text-amber-100">{t('MCPOAuth.productNotVerified')}</span>
                </div>
                <p className="mt-3 text-sm leading-6 text-zinc-300">{t('MCPOAuth.panel.openaiWeb.body')}</p>
                {renderCopyBlock('openai-web', 'MCPOAuth.copyPrompt')}
                <p className="mt-3 text-xs leading-5 text-amber-100">{t('MCPOAuth.panel.openaiWeb.evidence')}</p>
                {renderSource('openai-web')}
                {renderTechnicalDetails('mcp:query')}
              </section>
            </>
          )
            : tool.id === 'claude-code' ? renderSingleGuide('claude-code', 'claude-code')
              : renderSingleGuide('openclaw', 'openclaw')}
        </div>
      ))}
    </section>
  );
}
