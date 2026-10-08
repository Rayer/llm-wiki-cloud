import { readFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { validateRuntimeConfig } from '../../lib/runtime-config.ts';

export const runtime = 'nodejs';
export const dynamic = 'force-dynamic';

const headers = {
  'Cache-Control': 'no-store',
  'Content-Type': 'application/json; charset=utf-8',
};

export async function GET() {
  if (process.env.NODE_ENV !== 'development') return new Response(null, { status: 404, headers });

  try {
    const file = await readFile(resolve(process.cwd(), '../../.build/cac/local/frontend-config.json'), 'utf8');
    return Response.json(validateRuntimeConfig(JSON.parse(file)), { headers });
  } catch {
    return Response.json({ error: 'local frontend config unavailable' }, { status: 503, headers });
  }
}
