'use client';

import { useEffect, useState } from 'react';
import { Check, ChevronDown, Copy, Download, Folder } from 'lucide-react';
import { useT } from '@/lib/i18n';
import { useWorkspace } from '@/components/WorkspaceProvider';
import { getRuntimeConfig } from '@/lib/runtime-config';
import { createCLICommands, type CLIShell } from '@/lib/cli-command';
import { loadCLIReleaseManifest, type CLIReleaseManifest } from '@/lib/cli-release-metadata';

const platformTranslation: Record<string, string> = {
  'darwin/arm64': 'CLILane.platformDarwinArm64',
  'darwin/amd64': 'CLILane.platformDarwinAmd64',
  'windows/amd64': 'CLILane.platformWindowsAmd64',
  'linux/amd64': 'CLILane.platformLinuxAmd64',
};

export function CLILaneClient() {
  const { t } = useT();
  const { currentProject } = useWorkspace();
  const [openStep, setOpenStep] = useState<number | null>(null);
  const [shell, setShell] = useState<CLIShell>('posix');
  const [root, setRoot] = useState('');
  const [syncMissing, setSyncMissing] = useState(false);
  const [manifest, setManifest] = useState<CLIReleaseManifest | null>(null);
  const [metadataLoading, setMetadataLoading] = useState(true);
  const [copyStatus, setCopyStatus] = useState('');
  const authOrigin = getRuntimeConfig().auth_url;
  const projectId = currentProject?.id ?? '';
  const commands = createCLICommands({ shell, root, authOrigin, projectId });

  useEffect(() => {
    let active = true;
    void loadCLIReleaseManifest().then((release) => {
      if (active) {
        setManifest(release);
        setMetadataLoading(false);
      }
    });
    return () => { active = false; };
  }, []);

  const copy = async (key: string, command: string) => {
    if (!command) return;
    try {
      await navigator.clipboard.writeText(command);
      setCopyStatus(key);
      window.setTimeout(() => setCopyStatus(''), 1800);
    } catch {
      setCopyStatus('failed');
    }
  };

  const suggestedRoot = shell === 'powershell'
    ? 'C:\\Users\\your-name\\Documents\\YourWiki'
    : '/Users/your-name/Documents/YourWiki';
  const platformLabels = [
    ['darwin/arm64', t(platformTranslation['darwin/arm64'])],
    ['darwin/amd64', t(platformTranslation['darwin/amd64'])],
    ['windows/amd64', t(platformTranslation['windows/amd64'])],
    ['linux/amd64', t(platformTranslation['linux/amd64'])],
  ].filter(([target]) => shell === 'posix' ? target !== 'windows/amd64' : target === 'windows/amd64');
  const assets = manifest?.assets ?? [];

  return (
    <div className="mx-auto w-full max-w-4xl space-y-7 px-4 py-8 sm:px-6 lg:py-12">
      <header className="space-y-3">
        <p className="font-mono text-xs uppercase tracking-[0.2em] text-emerald-300">{t('CLILane.eyebrow')}</p>
        <h1 className="text-3xl font-semibold tracking-tight text-white sm:text-4xl">{t('CLILane.title')}</h1>
        <p className="max-w-3xl text-sm leading-6 text-zinc-300 sm:text-base">{t('CLILane.intro')}</p>
        <p className="max-w-3xl text-sm leading-6 text-zinc-400">{t('CLILane.agentNote')}</p>
        <p className="text-sm text-zinc-400">{t('CLILane.projectContext', { project: currentProject?.name ?? t('CLILane.noProject') })}</p>
      </header>

      <div className="flex flex-wrap items-center gap-2" role="group" aria-label={t('CLILane.shellLabel')}>
        <span className="mr-1 text-sm text-zinc-400">{t('CLILane.shellLabel')}</span>
        {(['posix', 'powershell'] as const).map((option) => (
          <button
            key={option}
            type="button"
            aria-pressed={shell === option}
            onClick={() => setShell(option)}
            className={`min-h-10 rounded-lg border px-3 text-sm transition focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-400 ${shell === option ? 'border-emerald-300/60 bg-emerald-300/10 text-emerald-100' : 'border-white/10 text-zinc-300 hover:bg-white/5'}`}
          >
            {option === 'posix' ? t('CLILane.posixShell') : t('CLILane.powershell')}
          </button>
        ))}
      </div>

      <ol className="space-y-3">
        <li>
          <StepCard number={1} title={t('CLILane.downloadTitle')} open={openStep === 1} onToggle={() => setOpenStep(openStep === 1 ? null : 1)}>
            <p className="mb-4 text-sm leading-6 text-zinc-300">{t('CLILane.downloadDescription')}</p>
            {metadataLoading ? (
              <p role="status" className="text-sm text-zinc-400">{t('CLILane.loadingMetadata')}</p>
            ) : (
              <div className="grid gap-3 sm:grid-cols-2">
                {platformLabels.map(([target, label]) => {
                  const asset = assets.find((item) => `${item.os}/${item.arch}` === target);
                  return (
                    <article key={target} className="rounded-xl border border-white/10 bg-black/20 p-4">
                      <h3 className="font-medium text-white">{label}</h3>
                      {asset?.download_url ? (
                        <>
                          <p className="mt-2 break-all font-mono text-xs text-zinc-400">{asset.asset_name}</p>
                          <a
                            href={asset.download_url}
                            target="_blank"
                            rel="noreferrer"
                            className="mt-3 inline-flex min-h-10 items-center gap-2 rounded-lg bg-emerald-300 px-3 text-sm font-semibold text-black hover:bg-emerald-200 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-400"
                          >
                            <Download aria-hidden="true" size={16} />{t('CLILane.download')}
                          </a>
                        </>
                      ) : (
                        <p className="mt-2 text-sm text-zinc-400">{metadataLoading ? t('CLILane.loadingMetadata') : t('CLILane.unavailable')}</p>
                      )}
                    </article>
                  );
                })}
              </div>
            )}
            {manifest ? <p className="mt-4 text-xs text-zinc-500">{t('CLILane.releaseVersion', { version: manifest.version })}</p> : null}
            <p className="mt-4 text-sm text-zinc-400">{t('CLILane.versionCommand')}</p>
            <CommandLine command={shell === 'powershell' ? '.\\lwc-sync.exe --version' : './lwc-sync --version'} label={t('CLILane.copy')} copied={copyStatus === 'version'} onCopy={() => void copy('version', shell === 'powershell' ? '.\\lwc-sync.exe --version' : './lwc-sync --version')} />
          </StepCard>
        </li>

        <li>
          <StepCard number={2} title={t('CLILane.loginTitle')} open={openStep === 2} onToggle={() => setOpenStep(openStep === 2 ? null : 2)}>
            <p className="mb-4 text-sm leading-6 text-zinc-300">{t('CLILane.loginDescription')}</p>
            <CommandLine command={commands.login} label={t('CLILane.copy')} copied={copyStatus === 'login'} onCopy={() => void copy('login', commands.login)} />
            <ul className="mt-4 list-disc space-y-2 pl-5 text-sm leading-6 text-zinc-400">
              <li>{t('CLILane.loginApproval')}</li>
              <li>{t('CLILane.loginCode')}</li>
              <li>{t('CLILane.loginComplete')}</li>
            </ul>
          </StepCard>
        </li>

        <li>
          <StepCard number={3} title={t('CLILane.folderTitle')} open={openStep === 3} onToggle={() => setOpenStep(openStep === 3 ? null : 3)}>
            <p className="mb-4 text-sm leading-6 text-zinc-300">{t('CLILane.folderDescription')}</p>
            <div className="flex flex-wrap items-end gap-3">
              <label htmlFor="cli-wiki-root" className="min-w-0 flex-1 text-sm font-medium text-zinc-200">
                {t('CLILane.rootLabel')}
                <input
                  id="cli-wiki-root"
                  value={root}
                  onChange={(event) => setRoot(event.target.value)}
                  placeholder={shell === 'powershell' ? 'C:\\Users\\you\\Documents\\YourWiki' : '/Users/you/Documents/YourWiki'}
                  autoComplete="off"
                  spellCheck={false}
                  className="mt-2 min-h-12 w-full rounded-lg border border-white/10 bg-black/30 px-4 py-3 font-mono text-sm text-white outline-none focus:border-emerald-300 focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-400"
                />
              </label>
              <button type="button" onClick={() => setRoot(suggestedRoot)} className="min-h-11 rounded-lg border border-white/15 px-3 text-sm text-zinc-200 hover:bg-white/5 focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-400">
                {t('CLILane.documentsExample')}
              </button>
            </div>
            <p className="mt-2 text-xs leading-5 text-zinc-500">{t('CLILane.rootHint')}</p>
            <p className="mt-4 flex items-start gap-2 text-sm leading-6 text-zinc-300"><Folder aria-hidden="true" className="mt-1 shrink-0 text-emerald-300" size={16} />{t('CLILane.rawScope')}</p>
            <p className="mt-3 text-sm leading-6 text-zinc-400">{t('CLILane.sameInit')}</p>
            <p className="mt-3 text-sm leading-6 text-zinc-400">{t('CLILane.recoveryGuidance')}</p>
            <CommandLine command={commands.init} label={t('CLILane.copy')} copied={copyStatus === 'init'} onCopy={() => void copy('init', commands.init)} />
            <details className="mt-5 rounded-lg border border-white/10 bg-black/20 p-4">
              <summary className="cursor-pointer text-sm font-medium text-zinc-200">{t('CLILane.obsidianTitle')}</summary>
              <p className="mt-3 text-sm leading-6 text-zinc-400">{t('CLILane.obsidianDescription')}</p>
            </details>
          </StepCard>
        </li>

        <li>
          <StepCard number={4} title={t('CLILane.syncTitle')} open={openStep === 4} onToggle={() => setOpenStep(openStep === 4 ? null : 4)}>
            <p className="mb-4 text-sm leading-6 text-zinc-300">{t('CLILane.syncDescription')}</p>
            <label className="flex min-h-11 items-center gap-3 text-sm text-zinc-200">
              <input type="checkbox" checked={syncMissing} onChange={(event) => setSyncMissing(event.target.checked)} className="size-4 accent-emerald-300" />
              {t('CLILane.syncOption')}
            </label>
            <CommandLine command={syncMissing ? commands.pushSync : commands.push} label={t('CLILane.copy')} copied={copyStatus === 'push'} onCopy={() => void copy('push', syncMissing ? commands.pushSync : commands.push)} />
            <details className="mt-5 rounded-lg border border-white/10 bg-black/20 p-4">
              <summary className="cursor-pointer text-sm font-medium text-zinc-200">{t('CLILane.syncDetailsTitle')}</summary>
              <ul className="mt-3 list-disc space-y-2 pl-5 text-sm leading-6 text-zinc-400">
                <li>{t('CLILane.syncConflict')}</li>
                <li>{t('CLILane.syncNoDelete')}</li>
                <li>{t('CLILane.syncNoCompile')}</li>
                <li>{t('CLILane.syncReady')}</li>
              </ul>
            </details>
          </StepCard>
        </li>
      </ol>
      {copyStatus === 'failed' ? <p role="alert" className="text-sm text-red-300">{t('CLILane.copyFailed')}</p> : null}
      <p className="sr-only" role="status" aria-live="polite">{copyStatus && copyStatus !== 'failed' ? t('CLILane.copied') : ''}</p>
    </div>
  );
}

function StepCard({ number, title, open, onToggle, children }: {
  number: number;
  title: string;
  open: boolean;
  onToggle: () => void;
  children: React.ReactNode;
}) {
  return (
    <section className="overflow-hidden rounded-2xl border border-white/10 bg-[#151515] shadow-lg shadow-black/10">
      <h2>
        <button
          type="button"
          aria-expanded={open}
          aria-controls={`cli-step-${number}`}
          onClick={onToggle}
          className="flex min-h-16 w-full items-center gap-4 px-4 py-4 text-left sm:px-5"
        >
          <span className="flex size-9 shrink-0 items-center justify-center rounded-full border border-emerald-300/30 font-mono text-sm text-emerald-200">{number}</span>
          <span className="flex-1 text-base font-semibold text-white sm:text-lg">{title}</span>
          <ChevronDown aria-hidden="true" className={`shrink-0 text-zinc-400 transition-transform ${open ? 'rotate-180' : ''}`} size={18} />
        </button>
      </h2>
      {open ? <div id={`cli-step-${number}`} className="border-t border-white/8 px-4 py-5 sm:px-5">{children}</div> : null}
    </section>
  );
}

function CommandLine({ command, label, copied, onCopy }: { command: string; label: string; copied: boolean; onCopy: () => void }) {
  const { t } = useT();
  return (
    <div className="mt-3 flex items-start gap-2 rounded-xl border border-white/10 bg-black/40 p-3">
      <code className="min-w-0 flex-1 whitespace-pre-wrap break-all font-mono text-xs leading-6 text-emerald-100 sm:text-sm">{command || t('CLILane.enterRoot')}</code>
      <button
        type="button"
        disabled={!command}
        aria-label={copied ? t('CLILane.copied') : label}
        onClick={onCopy}
        className="inline-flex min-h-10 min-w-10 shrink-0 items-center justify-center rounded-lg border border-white/10 text-zinc-300 hover:bg-white/10 disabled:cursor-not-allowed disabled:opacity-40 focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-400"
      >
        {copied ? <Check aria-hidden="true" size={16} /> : <Copy aria-hidden="true" size={16} />}
        <span className="sr-only">{copied ? t('CLILane.copied') : label}</span>
      </button>
    </div>
  );
}
