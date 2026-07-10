import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

import {
  formatQuotaLine,
  isRunBlocked,
} from '../src/lib/pipeline-quota.ts';

const baseQuota = {
  enforced: true,
  allowed: true,
  runs_today: 1,
  daily_limit: 3,
  new_raw_files: 2,
  min_new_raw: 1,
  already_running: false,
};

test('root layout declares Traditional Chinese document language', async () => {
  const layout = await readFile(new URL('../src/app/layout.tsx', import.meta.url), 'utf8');

  assert.match(layout, /<html lang="zh-Hant"/);
});

test('home latest entry cards omit the blurb when no description exists', async () => {
  const homeClient = await readFile(
    new URL('../src/components/HomeClient.tsx', import.meta.url),
    'utf8',
  );

  assert.doesNotMatch(
    homeClient,
    /Open this concept to start exploring the knowledge base\./,
  );
  assert.match(homeClient, /concept\.description \? \(/);
});

test('concept route remains but user-facing concept copy becomes entries', async () => {
  const [conceptsPage, homeClient, entryCard, detailClient, zhTW, en] = await Promise.all([
    readFile(new URL('../src/app/concepts/page.tsx', import.meta.url), 'utf8'),
    readFile(new URL('../src/components/HomeClient.tsx', import.meta.url), 'utf8'),
    readFile(new URL('../src/components/EntryCard.tsx', import.meta.url), 'utf8'),
    readFile(new URL('../src/components/DetailClient.tsx', import.meta.url), 'utf8'),
    readFile(new URL('../src/messages/zh-TW.json', import.meta.url), 'utf8').then(JSON.parse),
    readFile(new URL('../src/messages/en.json', import.meta.url), 'utf8').then(JSON.parse),
  ]);

  assert.match(conceptsPage, /basePath="\/concepts"/);
  assert.match(conceptsPage, /title=\{t\('List\.entriesTitle'\)\}/);
  assert.match(homeClient, /t\('Entry\.singular'\)/);
  assert.match(entryCard, /t\('Entry\.singular'\)/);
  assert.match(detailClient, /t\('Entry\.singular'\)/);
  assert.equal(zhTW.Shell.concepts, '條目');
  assert.equal(zhTW.Demo.latestConcepts, '最新條目');
  assert.equal(zhTW.Entry.singular, '條目');
  assert.equal(en.Shell.concepts, 'Entries');
  assert.equal(en.Entry.singular, 'Entry');
});

test('main-path chrome strings are i18n-backed in zh-TW', async () => {
  const [homeClient, pipelineClient, rawClient, listClient, commandPalette, zhTW] =
    await Promise.all([
      readFile(new URL('../src/components/HomeClient.tsx', import.meta.url), 'utf8'),
      readFile(new URL('../src/components/PipelineClient.tsx', import.meta.url), 'utf8'),
      readFile(new URL('../src/components/RawClient.tsx', import.meta.url), 'utf8'),
      readFile(new URL('../src/components/ListClient.tsx', import.meta.url), 'utf8'),
      readFile(new URL('../src/components/ui/CommandPalette.tsx', import.meta.url), 'utf8'),
      readFile(new URL('../src/messages/zh-TW.json', import.meta.url), 'utf8').then(JSON.parse),
    ]);

  assert.match(homeClient, /label=\{t\('Shell\.sources'\)\}/);
  assert.match(homeClient, /label=\{t\('Shell\.concepts'\)\}/);
  assert.match(homeClient, /label=\{t\('Shell\.raw'\)\}/);
  assert.match(pipelineClient, /t\('Pipeline\.addContent'\)/);
  assert.match(pipelineClient, /t\('Pipeline\.uploadFiles'\)/);
  assert.match(pipelineClient, /t\('Pipeline\.scrapeUrl'\)/);
  assert.match(pipelineClient, /t\('Pipeline\.runPipeline'\)/);
  assert.match(rawClient, /useT\(\)/);
  assert.match(listClient, /placeholder=\{t\('List\.searchPlaceholder'/);
  assert.match(commandPalette, /labels\.sources/);
  assert.match(commandPalette, /labels\.concepts/);
  assert.equal(zhTW.Shell.raw, '原文');
  assert.equal(zhTW.Pipeline.addContent, '新增內容');
  assert.equal(zhTW.Pipeline.runPipeline, '執行 Pipeline');
});

test('quota line can be formatted with i18n templates', () => {
  const now = new Date('2026-07-10T12:00:00.000Z');
  const messages = {
    quotaLine: '今日執行：{runs}/{limit} · 冷卻：{cooldown} · 新檔案：{newFiles}',
    quotaNotEnforced: '未啟用配額限制',
    cooldownClear: '無',
  };

  assert.equal(
    formatQuotaLine(baseQuota, now, messages),
    '今日執行：1/3 · 冷卻：無 · 新檔案：2',
  );
  assert.equal(
    formatQuotaLine({ ...baseQuota, enforced: false }, now, messages),
    '未啟用配額限制',
  );
});

test('terminal executions do not stay blocked solely because quota says already_running', async () => {
  const pipelineClient = await readFile(
    new URL('../src/components/PipelineClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(pipelineClient, /lastExecutionStatus === 'RUNNING'/);
  assert.match(pipelineClient, /staleAlreadyRunning/);
  assert.equal(
    isRunBlocked({
      isDemoSession: false,
      loading: false,
      hasProject: true,
      executionRunning: false,
      quota: { ...baseQuota, allowed: true, already_running: true },
    }),
    false,
  );
});
