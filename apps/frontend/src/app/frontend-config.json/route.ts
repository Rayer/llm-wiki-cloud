import { readFile } from 'node:fs/promises';
import { resolve } from 'node:path';

export const runtime = 'nodejs';
export const dynamic = 'force-dynamic';

const headers = {
  'Cache-Control': 'no-store',
  'Content-Type': 'application/json; charset=utf-8',
};

function sanitizePublicConfig(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return {};
  const record = value as Record<string, unknown>;
  const allowed = ['schema_version', 'api_url', 'auth_url'];
  if (Object.keys(record).some((key) => !allowed.includes(key))) {
    throw new Error('unexpected local config field');
  }

  const result: Record<string, unknown> = {};
  if ('schema_version' in record) {
    result.schema_version = typeof record.schema_version === 'number' ? record.schema_version : null;
  }
  for (const key of ['api_url', 'auth_url']) {
    if (key in record) result[key] = typeof record[key] === 'string' ? record[key] : null;
  }
  return result;
}

export async function GET() {
  if (process.env.NODE_ENV !== 'development') return new Response(null, { status: 404, headers });

  let file: string;
  try {
    file = await readFile(resolve(process.cwd(), '../../.build/cac/local/frontend-config.json'), 'utf8');
  } catch {
    return Response.json({ error: 'local frontend config unavailable' }, { status: 503, headers });
  }

  let parsed: unknown;
  try {
    parsed = JSON.parse(file);
  } catch {
    return Response.json({}, { headers });
  }

  try {
    return Response.json(sanitizePublicConfig(parsed), { headers });
  } catch {
    return Response.json({ error: 'local frontend config unavailable' }, { status: 503, headers });
  }
}
