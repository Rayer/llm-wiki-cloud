// @vitest-environment node

import { createServer, type IncomingMessage, type ServerResponse } from 'node:http';
import { once } from 'node:events';
import type { AddressInfo } from 'node:net';
import { afterEach, describe, expect, it } from 'vitest';
import { clearRuntimeConfigForTests, setRuntimeConfigForTests } from '@/lib/runtime-config';
import { decideMCPOAuthConsent, readMCPOAuthConsent } from '@/lib/mcp-oauth';

const transactionId = `lwc_mcp_tx_${'H'.repeat(43)}`;
const accessToken = 'synthetic-fixture-token';

type CapturedRequest = { method: string; path: string; authorization: string | null; body: string };

function send(response: ServerResponse, status: number, value: unknown) {
  response.writeHead(status, { 'Content-Type': 'application/json' });
  response.end(JSON.stringify(value));
}

async function readBody(request: IncomingMessage): Promise<string> {
  const chunks: Buffer[] = [];
  for await (const chunk of request) chunks.push(Buffer.from(chunk));
  return Buffer.concat(chunks).toString('utf8');
}

describe('LWC-468 consent helper HTTP contract', () => {
  afterEach(() => clearRuntimeConfigForTests());

  it('classifies a project template with Auth, reads and decides through BFF schema_version 1', async () => {
    const requests: CapturedRequest[] = [];
    const server = createServer(async (request, response) => {
      const body = await readBody(request);
      const path = request.url ?? '';
      requests.push({
        method: request.method ?? 'GET',
        path,
        authorization: request.headers.authorization ?? null,
        body,
      });

      if (path === `/api/v1/auth/mcp/oauth/consent/${transactionId}` && request.method === 'GET') {
        send(response, 200, { resource_kind: 'project-template' });
        return;
      }
      if (path === `/api/v1/mcp/oauth/consent/${transactionId}` && request.method === 'GET') {
        send(response, 200, {
          transaction_id: transactionId,
          account_email: 'fixture-account@example.test',
          project_id: 'synthetic-project',
          project_name: 'Synthetic fixture project',
          client_name: 'Synthetic MCP client',
          resource: 'https://api.fixture.test/mcp/projects/synthetic-project',
          scope: 'mcp:query',
          expires_at: '2026-11-04T00:00:00Z',
        });
        return;
      }
      if (path === `/api/v1/mcp/oauth/consent/${transactionId}/decision` && request.method === 'POST') {
        send(response, 200, { requires_consent: true });
        return;
      }
      send(response, 404, { error: 'unexpected fixture route' });
    });

    server.listen(0, '127.0.0.1');
    await once(server, 'listening');
    const origin = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
    setRuntimeConfigForTests({ schema_version: 1, api_url: origin, auth_url: origin });

    try {
      const consent = await readMCPOAuthConsent(transactionId, accessToken);
      expect(consent.client_name).toBe('Synthetic MCP client');
      expect(consent.project_id).toBe('synthetic-project');
      const decision = await decideMCPOAuthConsent(transactionId, 'continue', accessToken);
      expect(decision).toEqual({ requires_consent: true });
      expect(requests).toEqual([
        {
          method: 'GET',
          path: `/api/v1/auth/mcp/oauth/consent/${transactionId}`,
          authorization: `Bearer ${accessToken}`,
          body: '',
        },
        {
          method: 'GET',
          path: `/api/v1/mcp/oauth/consent/${transactionId}`,
          authorization: `Bearer ${accessToken}`,
          body: '',
        },
        {
          method: 'GET',
          path: `/api/v1/auth/mcp/oauth/consent/${transactionId}`,
          authorization: `Bearer ${accessToken}`,
          body: '',
        },
        {
          method: 'POST',
          path: `/api/v1/mcp/oauth/consent/${transactionId}/decision`,
          authorization: `Bearer ${accessToken}`,
          body: JSON.stringify({ schema_version: 1, decision: 'continue' }),
        },
      ]);
    } finally {
      await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()));
    }
  });
});
