import assert from 'node:assert/strict';
import test from 'node:test';
import { recordRequestIDHeader, recordRequestResponse, recentRequestSnapshot, subscribeRecentRequests } from '../src/lib/request-ids.ts';

test('recent request ledger accepts only actual UUID response headers and stays bounded', () => {
  let notifications = 0;
  const unsubscribe = subscribeRecentRequests(() => { notifications += 1; });
  recordRequestIDHeader('caller-controlled-id', 200);
  recordRequestIDHeader('123e4567-e89b-12d3-a456-426614174000', 700);
  assert.equal(recentRequestSnapshot().length, 0);

  for (let index = 1; index <= 9; index += 1) {
    const id = `123e4567-e89b-12d3-a456-${String(index).padStart(12, '0')}`;
    const response = recordRequestResponse(new Response('{}', {
      status: index === 9 ? 503 : 200,
      headers: { 'X-Request-ID': id },
    }));
    assert.equal(response.status, index === 9 ? 503 : 200);
  }

  const entries = recentRequestSnapshot();
  assert.equal(entries.length, 8);
  assert.equal(entries[0].id, '123e4567-e89b-12d3-a456-000000000009');
  assert.equal(entries[0].status, 503);
  assert.equal(entries.at(-1).id, '123e4567-e89b-12d3-a456-000000000002');
  assert.equal(notifications, 9);
  unsubscribe();
});
