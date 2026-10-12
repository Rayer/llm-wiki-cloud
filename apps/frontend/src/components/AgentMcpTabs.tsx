'use client';

import { useRef, useState } from 'react';

type Translate = (key: string, params?: Record<string, string | number>) => string;
type ToolId = 'hermes' | 'codex' | 'grok' | 'claude-code' | 'openclaw';

const tools: { id: ToolId; labelKey: string }[] = [
  { id: 'hermes', labelKey: 'MCPOAuth.toolHermes' },
  { id: 'codex', labelKey: 'MCPOAuth.toolCodex' },
  { id: 'grok', labelKey: 'MCPOAuth.toolGrok' },
  { id: 'claude-code', labelKey: 'MCPOAuth.toolClaudeCode' },
  { id: 'openclaw', labelKey: 'MCPOAuth.toolOpenClaw' },
];

function tabId(id: ToolId) { return `agent-mcp-tab-${id}`; }
function panelId(id: ToolId) { return `agent-mcp-panel-${id}`; }

export function AgentMcpTabs({
  resourceURL,
  configSnippet,
  environmentLabel,
  t,
}: {
  resourceURL: string;
  configSnippet: string;
  environmentLabel: string;
  t: Translate;
}) {
  const [selectedTool, setSelectedTool] = useState<ToolId>('hermes');
  const [copied, setCopied] = useState(false);
  const [copyError, setCopyError] = useState(false);
  const tabRefs = useRef<Array<HTMLButtonElement | null>>([]);

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

  const copyConfig = async () => {
    setCopyError(false);
    try {
      await navigator.clipboard.writeText(configSnippet);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 2000);
    } catch {
      setCopied(false);
      setCopyError(true);
    }
  };

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
              <pre className="mt-4 overflow-x-auto rounded-lg border border-white/10 bg-black/30 p-4 text-xs leading-5 text-zinc-200"><code>{configSnippet}</code></pre>
              <div className="mt-3 flex flex-wrap items-center gap-3">
                <button type="button" onClick={() => void copyConfig()} className="min-h-11 rounded-lg bg-emerald-300 px-4 py-2 text-sm font-semibold text-black hover:bg-emerald-200">{copied ? t('MCPOAuth.copied') : t('MCPOAuth.copyConfig')}</button>
                <span role="status" className="text-sm text-zinc-400">{copyError ? t('MCPOAuth.copyFailed') : t('MCPOAuth.nativeCheck')}</span>
              </div>
              <details className="mt-4 rounded-lg border border-white/8 bg-black/15 px-4 py-3">
                <summary className="cursor-pointer text-sm text-zinc-300 focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-300">{t('MCPOAuth.technicalDetails')}</summary>
                <dl className="mt-3 grid gap-3 text-sm sm:grid-cols-2">
                  <div><dt className="text-xs text-zinc-500">{t('MCPOAuth.environment')}</dt><dd className="mt-1 text-zinc-200">{environmentLabel}</dd></div>
                  <div><dt className="text-xs text-zinc-500">{t('MCPOAuth.scope')}</dt><dd className="mt-1 font-mono text-emerald-200">mcp:query</dd></div>
                  <div className="sm:col-span-2"><dt className="text-xs text-zinc-500">{t('MCPOAuth.resource')}</dt><dd className="mt-1 break-all font-mono text-xs leading-5 text-zinc-300">{resourceURL}</dd></div>
                </dl>
              </details>
            </div>
          ) : (
            <div>
              <div className="flex flex-wrap items-start justify-between gap-3">
                <h3 className="text-lg font-semibold text-white">{t(`MCPOAuth.panel.${tool.id}.title`)}</h3>
                <span className="rounded-md border border-white/10 px-2.5 py-1 text-xs text-zinc-300">{t('MCPOAuth.guidancePreparing')}</span>
              </div>
              <div className="mt-4 rounded-xl border border-dashed border-white/15 bg-black/15 p-5 sm:p-6">
                <h4 className="font-semibold text-zinc-100">{t('MCPOAuth.guidancePreparing')}</h4>
                <p className="mt-2 text-sm leading-6 text-zinc-400">{t(`MCPOAuth.panel.${tool.id}.body`)}</p>
              </div>
              <p className="mt-3 text-xs leading-5 text-amber-200">{t('MCPOAuth.placeholderDisclaimer')}</p>
            </div>
          )}
        </div>
      ))}
    </section>
  );
}
