import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

import {
  blockReasonMessage,
  formatCooldownRemaining,
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

test('api.ts defines PipelineQuota and extends status/result types', async () => {
  const api = await readFile(new URL('../src/lib/api.ts', import.meta.url), 'utf8');

  assert.match(api, /export type PipelineQuota = \{/);
  assert.match(api, /enforced: boolean;/);
  assert.match(api, /allowed: boolean;/);
  assert.match(api, /runs_today: number;/);
  assert.match(api, /daily_limit: number;/);
  assert.match(api, /cooldown_until\?: string \| null;/);
  assert.match(api, /new_raw_files: number;/);
  assert.match(api, /min_new_raw: number;/);
  assert.match(api, /already_running: boolean;/);
  assert.match(api, /quota\?: PipelineQuota \| null;/);
  assert.match(api, /project_id\?: string;/);
  assert.match(api, /execution_id\?: string;/);
  assert.match(api, /export async function getPipelineStatus\(\)/);
  assert.match(api, /export async function triggerPipeline\(\)/);
});

test('pipeline-quota.ts exports format/block helpers', async () => {
  const src = await readFile(new URL('../src/lib/pipeline-quota.ts', import.meta.url), 'utf8');

  assert.match(src, /export function formatQuotaLine/);
  assert.match(src, /export function formatCooldownRemaining/);
  assert.match(src, /export function isRunBlocked/);
  assert.match(src, /export function blockReasonMessage/);
  assert.match(src, /import type \{ PipelineQuota \} from '\.\/api'/);
});

test('formatCooldownRemaining returns null for empty, invalid, or past times', () => {
  const now = new Date('2026-07-10T12:00:00.000Z');
  assert.equal(formatCooldownRemaining(null, now), null);
  assert.equal(formatCooldownRemaining(undefined, now), null);
  assert.equal(formatCooldownRemaining('not-a-date', now), null);
  assert.equal(formatCooldownRemaining('2026-07-10T11:00:00.000Z', now), null);
  assert.equal(formatCooldownRemaining('2026-07-10T12:00:00.000Z', now), null);
});

test('formatCooldownRemaining formats minutes and hours', () => {
  const now = new Date('2026-07-10T12:00:00.000Z');
  assert.equal(formatCooldownRemaining('2026-07-10T12:00:30.000Z', now), '1m');
  assert.equal(formatCooldownRemaining('2026-07-10T12:15:00.000Z', now), '15m');
  assert.equal(formatCooldownRemaining('2026-07-10T13:00:00.000Z', now), '1h');
  assert.equal(formatCooldownRemaining('2026-07-10T13:30:00.000Z', now), '1h 30m');
  assert.equal(formatCooldownRemaining('2026-07-10T14:00:00.000Z', now), '2h');
});

test('formatQuotaLine handles missing, unenforced, and enforced quota', () => {
  const now = new Date('2026-07-10T12:00:00.000Z');
  assert.equal(formatQuotaLine(null, now), '');
  assert.equal(formatQuotaLine(undefined, now), '');
  assert.equal(
    formatQuotaLine({ ...baseQuota, enforced: false }, now),
    'Quota not enforced',
  );
  assert.equal(
    formatQuotaLine(baseQuota, now),
    'Runs today: 1/3 · Cooldown: clear · New files: 2',
  );
  assert.equal(
    formatQuotaLine(
      { ...baseQuota, cooldown_until: '2026-07-10T12:45:00.000Z' },
      now,
    ),
    'Runs today: 1/3 · Cooldown: 45m · New files: 2',
  );
});

test('isRunBlocked covers demo, loading, project, running, and quota denial', () => {
  const free = {
    isDemoSession: false,
    loading: false,
    hasProject: true,
    executionRunning: false,
    quota: baseQuota,
  };
  assert.equal(isRunBlocked(free), false);
  assert.equal(isRunBlocked({ ...free, isDemoSession: true }), true);
  assert.equal(isRunBlocked({ ...free, loading: true }), true);
  assert.equal(isRunBlocked({ ...free, hasProject: false }), true);
  assert.equal(isRunBlocked({ ...free, executionRunning: true }), true);
  assert.equal(
    isRunBlocked({
      ...free,
      quota: { ...baseQuota, allowed: false },
    }),
    true,
  );
  assert.equal(
    isRunBlocked({
      ...free,
      quota: { ...baseQuota, enforced: false, allowed: false },
    }),
    false,
  );
  assert.equal(isRunBlocked({ ...free, quota: null }), false);
});

test('blockReasonMessage prefers demo, project, running, then quota message', () => {
  const opts = {
    isDemoSession: false,
    hasProject: true,
    executionRunning: false,
    quota: null,
    demoMessage: 'Demo blocked',
    noProjectMessage: 'Pick a project',
  };
  assert.equal(blockReasonMessage({ ...opts, isDemoSession: true }), 'Demo blocked');
  assert.equal(blockReasonMessage({ ...opts, hasProject: false }), 'Pick a project');
  assert.equal(
    blockReasonMessage({ ...opts, executionRunning: true }),
    'Pipeline is running',
  );
  assert.equal(
    blockReasonMessage({
      ...opts,
      executionRunning: true,
      quota: { ...baseQuota, message: 'Still running' },
    }),
    'Still running',
  );
  assert.equal(
    blockReasonMessage({
      ...opts,
      quota: { ...baseQuota, allowed: false, message: 'Daily limit reached' },
    }),
    'Daily limit reached',
  );
  assert.equal(blockReasonMessage(opts), '');
});

test('PipelineClient wires quota line, disabled Run, status fetch, and checklist', async () => {
  const pipelineClient = await readFile(
    new URL('../src/components/PipelineClient.tsx', import.meta.url),
    'utf8',
  );

  assert.match(pipelineClient, /from '@\/lib\/pipeline-quota'/);
  assert.match(pipelineClient, /formatQuotaLine/);
  assert.match(pipelineClient, /isRunBlocked/);
  assert.match(pipelineClient, /blockReasonMessage/);
  assert.match(pipelineClient, /data-testid="pipeline-quota-line"/);
  assert.match(pipelineClient, /disabled=\{blocked\}/);
  assert.match(pipelineClient, /getPipelineStatus/);
  assert.match(pipelineClient, /isDemoSession/);
  assert.match(pipelineClient, /showPrereq/);
  assert.match(pipelineClient, /useState\(false\)/);
  assert.match(pipelineClient, /already_running/);
  assert.match(pipelineClient, /currentProject/);
  // After upload created: refresh status once in addition to nav counts.
  assert.match(pipelineClient, /needsCountRefreshRef[\s\S]*?getPipelineStatus/);
  // Demo early-return remains belt-and-suspenders on click.
  assert.match(pipelineClient, /handleRunPipeline[\s\S]*?isDemoSession/);
  // Toast path for API/race errors.
  assert.match(pipelineClient, /err instanceof Error \? err\.message/);
});

test('locales expose Pipeline quota copy', async () => {
  const [english, traditionalChinese] = await Promise.all([
    readFile(new URL('../src/messages/en.json', import.meta.url), 'utf8').then(JSON.parse),
    readFile(new URL('../src/messages/zh-TW.json', import.meta.url), 'utf8').then(JSON.parse),
  ]);

  for (const messages of [english, traditionalChinese]) {
    assert.equal(typeof messages.Pipeline.quotaLine, 'string');
    assert.equal(typeof messages.Pipeline.quotaNotEnforced, 'string');
    assert.equal(typeof messages.Pipeline.cooldownClear, 'string');
    assert.equal(typeof messages.Pipeline.noProject, 'string');
    assert.equal(typeof messages.Pipeline.prerequisites, 'string');
    assert.equal(typeof messages.Pipeline.prereqDemo, 'string');
    assert.equal(typeof messages.Pipeline.prereqDaily, 'string');
    assert.equal(typeof messages.Pipeline.prereqCooldown, 'string');
    assert.equal(typeof messages.Pipeline.prereqRunning, 'string');
    assert.equal(typeof messages.Pipeline.prereqRaw, 'string');
  }

  assert.match(english.Pipeline.quotaLine, /\{runs\}/);
  assert.match(english.Pipeline.quotaLine, /\{limit\}/);
  assert.equal(english.Pipeline.quotaNotEnforced, 'Quota not enforced');
});
