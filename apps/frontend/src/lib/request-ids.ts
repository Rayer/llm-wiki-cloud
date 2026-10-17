export type RecentRequest = { id: string; status: number; observedAt: number };

const MAX_RECENT_REQUESTS = 8;
const requestIDPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
const emptySnapshot: readonly RecentRequest[] = Object.freeze([]);
let recentRequests: readonly RecentRequest[] = emptySnapshot;
const listeners = new Set<() => void>();

export function recordRequestIDHeader(value: string | null, status: number): void {
  if (!value || !requestIDPattern.test(value) || !Number.isInteger(status) || status < 0 || status > 599) return;
  recentRequests = [
    { id: value, status, observedAt: Date.now() },
    ...recentRequests,
  ].slice(0, MAX_RECENT_REQUESTS);
  for (const listener of listeners) listener();
}

export function recordRequestResponse(response: Response): Response {
  recordRequestIDHeader(response.headers?.get?.('X-Request-ID') ?? null, response.status);
  return response;
}

export function recentRequestSnapshot(): readonly RecentRequest[] {
  return recentRequests;
}

export function subscribeRecentRequests(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}
