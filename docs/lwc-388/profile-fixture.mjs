import { createServer } from 'node:http';

const host = '127.0.0.1';
const port = Number(process.env.LWC388_FIXTURE_PORT || 8089);
const allowedOrigins = new Set(['http://127.0.0.1:3000', 'http://localhost:3000']);
const projectId = 'profile-fixture';
const project = { id: projectId, name: 'LWC-388 本機示例專案' };

let profile = {
  project_id: projectId,
  revision: 1,
  requirements: [],
  derivation_status: null,
  scheduled_for: null,
  derivation_error_code: null,
  candidate: null,
  confirmed_candidate_id: null,
  active: null,
  job: null,
};

function sendJson(response, status, payload) {
  response.writeHead(status, { 'Content-Type': 'application/json; charset=utf-8' });
  response.end(JSON.stringify(payload));
}

const server = createServer(async (request, response) => {
  const origin = request.headers.origin;
  if (typeof origin === 'string' && allowedOrigins.has(origin)) {
    response.setHeader('Access-Control-Allow-Origin', origin);
    response.setHeader('Access-Control-Allow-Credentials', 'true');
    response.setHeader('Vary', 'Origin');
  }
  response.setHeader('Access-Control-Allow-Headers', 'Authorization, Content-Type, X-Project-Id');
  response.setHeader('Access-Control-Allow-Methods', 'GET, PUT, POST, OPTIONS');

  if (request.method === 'OPTIONS') {
    response.writeHead(204);
    response.end();
    return;
  }

  const url = new URL(request.url || '/', `http://${host}:${port}`);
  const { method } = request;
  console.log(`${method} ${url.pathname}`);

  if (url.pathname === '/api/v1/auth/refresh' && method === 'POST') {
    sendJson(response, 401, { error: 'Fixture auth is not connected; use the documented local browser session.' });
    return;
  }
  if (url.pathname === '/api/v1/public/config' && method === 'GET') {
    sendJson(response, 200, {
      registration_enabled: false,
      email_registration_enabled: false,
      google_registration_enabled: false,
      demo_enabled: false,
    });
    return;
  }

  if (request.headers.authorization !== 'Bearer fixture-access-token') {
    sendJson(response, 401, { error: 'Local fixture token required.' });
    return;
  }

  if (url.pathname === '/api/v1/projects' && method === 'GET') {
    sendJson(response, 200, { projects: [project] });
    return;
  }
  if (url.pathname === '/api/v1/status' && method === 'GET') {
    sendJson(response, 200, { sources_count: 0, concepts_count: 0, raw_count: 0 });
    return;
  }
  if (url.pathname === `/api/v1/projects/${projectId}/keys` && method === 'GET') {
    sendJson(response, 200, { keys: [] });
    return;
  }
  if (url.pathname === `/api/v1/projects/${projectId}/profile` && method === 'GET') {
    sendJson(response, 200, profile);
    return;
  }
  if (url.pathname === `/api/v1/projects/${projectId}/profile/bootstrap-guidance` && method === 'GET') {
    sendJson(response, 200, { bootstrap_guidance: null });
    return;
  }
  if (url.pathname === `/api/v1/projects/${projectId}/profile` && method === 'PUT') {
    let body = {};
    try {
      const chunks = [];
      for await (const chunk of request) chunks.push(chunk);
      body = JSON.parse(Buffer.concat(chunks).toString('utf8'));
    } catch {
      sendJson(response, 400, { error: 'Invalid local fixture request body.' });
      return;
    }
    if (body.expected_revision !== profile.revision || !Array.isArray(body.requirements)) {
      sendJson(response, 409, { error: 'Local fixture revision conflict.' });
      return;
    }
    profile = {
      ...profile,
      revision: profile.revision + 1,
      requirements: body.requirements,
      derivation_status: body.requirements.length > 0 ? 'pending' : null,
      scheduled_for: body.requirements.length > 0 ? new Date(Date.now() + 3 * 60 * 1000).toISOString() : null,
    };
    sendJson(response, 200, profile);
    return;
  }

  sendJson(response, 404, { error: 'No such local fixture route.' });
});

server.listen(port, host, () => {
  console.log(`LWC-388 isolated Profile fixture listening on http://${host}:${port}`);
  console.log('All API data is synthetic and stored only in this process memory.');
});

for (const signal of ['SIGINT', 'SIGTERM']) {
  process.on(signal, () => server.close(() => process.exit(0)));
}
