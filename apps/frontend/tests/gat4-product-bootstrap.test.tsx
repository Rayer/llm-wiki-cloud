import { afterEach, expect, it, vi } from 'vitest';
import { bootstrapDefaultProject } from '../src/lib/projects.ts';

function jsonResponse(payload: unknown, status = 200): Response {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
});

it('sends only the authenticated default-project bootstrap request and validates its strict response', async () => {
  const fetchMock = vi.fn(async () => jsonResponse({
    schema_version: 1,
    status: 'ready',
    default_project_id: 'default',
  }));
  vi.stubGlobal('fetch', fetchMock);

  await expect(bootstrapDefaultProject('web-access-token')).resolves.toBe('default');

  expect(fetchMock).toHaveBeenCalledOnce();
  const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
  expect(url).toBe('https://api.runtime.test/api/v1/onboarding/default-project');
  expect(init.method).toBe('POST');
  expect(init.credentials).toBe('omit');
  expect(init.cache).toBe('no-store');
  expect(new Headers(init.headers).get('Authorization')).toBe('Bearer web-access-token');
  expect(new Headers(init.headers).get('X-Project-ID')).toBeNull();
  expect(init.body).toBe('{"schema_version":1}');
});

it('rejects non-ready, unknown-field, and invalid project locators', async () => {
  for (const payload of [
    { schema_version: 1, status: 'pending', default_project_id: 'default' },
    { schema_version: 1, status: 'ready', default_project_id: 'default', subject: 'caller-input' },
    { schema_version: 1, status: 'ready', default_project_id: '../other' },
  ]) {
    vi.stubGlobal('fetch', vi.fn(async () => jsonResponse(payload)));
    await expect(bootstrapDefaultProject('web-access-token')).rejects.toThrow('Workspace setup response is invalid.');
  }
});
