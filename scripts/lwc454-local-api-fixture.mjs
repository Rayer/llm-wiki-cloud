import { createServer } from 'node:http';

const host = '127.0.0.1';
const port = Number(process.env.LWC454_FIXTURE_PORT ?? 4174);
const projectId = 'fixture-project-1';
const sourcePath = 'raw/meeting-notes.md';
const markdown = `# Synthetic source heading\n\nA paragraph directly before a table.\n\n| Topic | Detail |\n| --- | --- |\n| Status | Synthetic local fixture |\n\n## Notes\n\n- Preserve the source link\n- Keep unknowns explicit\n\n\`\`\`text\nfixture only\n\`\`\``;
const state = {
  bootstrap: 'unavailable',
  imported: null,
  finishImport: false,
  includeFailure: false,
  deletedFailure: false,
  description: 'Local fixture project description',
  descriptionRevision: 2,
  automation: { enabled: false, revision: 4 },
  requests: [],
};

const failureRecord = () => ({
  document_id: 'fixture-failed-import-1', original_filename: 'failed-review.docx',
  media_type: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document', bytes: 2310,
  sha256: 'c'.repeat(64), created_at: '2026-10-31T02:00:00Z', updated_at: '2026-10-31T02:01:00Z', revision: 3,
  converter_version: 'local-synthetic-converter', conversion_state: 'failed', source_state: 'pending',
  failure: { safe_message_key: 'conversion_error', diagnostic_id: 'fixture-failure-1' },
  pipeline_ref: { status: 'not_started_disabled' }, deletion_state: state.deletedFailure ? 'deleted' : 'present',
});

function importedRecord() {
  if (!state.imported) return null;
  const done = state.finishImport;
  return {
    document_id: state.imported.documentId, original_filename: state.imported.filename,
    media_type: state.imported.mediaType, bytes: state.imported.bytes, sha256: 'd'.repeat(64),
    created_at: '2026-10-31T02:10:00Z', updated_at: '2026-10-31T02:11:00Z', revision: done ? 2 : 1,
    converter_version: 'local-synthetic-converter', conversion_state: done ? 'succeeded' : 'converting',
    source_state: done ? 'saved' : 'not_started',
    ...(done ? { source_ref: { raw_path: sourcePath, sha256: 'e'.repeat(64), object_generation: 1 } } : {}),
    pipeline_ref: { status: 'not_started_disabled' }, deletion_state: 'present',
  };
}

function send(res, status, payload, contentType = 'application/json; charset=utf-8') {
  res.writeHead(status, {
    'Content-Type': contentType,
    'Access-Control-Allow-Origin': 'http://127.0.0.1:4173',
    'Access-Control-Allow-Headers': 'Authorization, Content-Type, X-Project-ID, If-Match, Idempotency-Key',
    'Access-Control-Allow-Methods': 'GET, POST, PUT, DELETE, OPTIONS',
    'Access-Control-Allow-Credentials': 'true',
    'Cache-Control': 'no-store',
    'X-Request-ID': `fixture-${Date.now().toString(36)}`,
  });
  if (status === 204) return res.end();
  res.end(contentType.startsWith('application/json') ? JSON.stringify(payload) : payload);
}

function readJSON(req) {
  return new Promise((resolve) => {
    let body = '';
    req.on('data', (chunk) => { body += chunk; });
    req.on('end', () => {
      try { resolve(JSON.parse(body || '{}')); } catch { resolve({}); }
    });
  });
}

function record(req, url) {
  state.requests.push({ method: req.method, path: url.pathname, status: 0 });
  if (state.requests.length > 300) state.requests.shift();
  return state.requests.at(-1);
}

const server = createServer(async (req, res) => {
  if (req.method === 'OPTIONS') return send(res, 204, null);
  const url = new URL(req.url ?? '/', `http://${host}:${port}`);
  if (url.pathname.startsWith('/__fixture/')) {
    const item = record(req, url);
    if (req.method === 'POST' && url.pathname === '/__fixture/state') {
      const input = await readJSON(req);
      for (const key of ['bootstrap', 'finishImport', 'includeFailure']) {
        if (key in input) state[key] = input[key];
      }
      if (input.reset === true) {
        state.imported = null; state.finishImport = false; state.includeFailure = false; state.deletedFailure = false;
        state.requests = [];
      }
      item.status = 200;
      return send(res, 200, { ok: true, state: { bootstrap: state.bootstrap, finishImport: state.finishImport, includeFailure: state.includeFailure } });
    }
    item.status = 200;
    return send(res, 200, { bootstrap: state.bootstrap, finishImport: state.finishImport, includeFailure: state.includeFailure, deletedFailure: state.deletedFailure, requests: state.requests });
  }

  const entry = record(req, url);
  const method = req.method ?? 'GET';
  const path = url.pathname;
  const projectPath = `/api/v1/projects/${projectId}`;
  let status = 200;
  let payload = {};
  let contentType = 'application/json; charset=utf-8';

  if (method === 'POST' && path === '/api/v1/onboarding/default-project') {
    if (state.bootstrap === 'unavailable') { status = 503; payload = { error: 'account access unavailable' }; }
    else if (state.bootstrap === 'unauthorized') { status = 401; payload = { error: 'invalid token' }; }
    else payload = { schema_version: 1, status: 'ready', default_project_id: projectId };
  } else if (method === 'GET' && path === '/api/v1/projects') {
    payload = [{ id: projectId, name: 'Local Sample Project', description: state.description, description_revision: state.descriptionRevision }];
  } else if (method === 'GET' && path === '/api/v1/status') {
    payload = { sources_count: state.finishImport ? 2 : 1, concepts_count: 2, raw_count: state.finishImport ? 2 : 1, suggested_queries: [] };
  } else if (method === 'GET' && path === '/api/v1/concepts') {
    payload = [];
  } else if (method === 'GET' && path === '/api/v1/document-imports/failure-summary') {
    payload = { failed_count: state.includeFailure && !state.deletedFailure ? 1 : 0, latest_failure_sequence: state.includeFailure ? 1 : 0, dismissed_through_sequence: 0 };
  } else if (path === '/api/v1/onboarding/default-project' && method === 'GET') {
    payload = { error: 'method not allowed' }; status = 405;
  } else if (method === 'GET' && path === `${projectPath}/profile/bootstrap-guidance`) {
    payload = { bootstrap_guidance: null };
  } else if (method === 'GET' && path === `${projectPath}/profile`) {
    payload = { project_id: projectId, revision: 3, requirements: [{ id: 'fixture-req-1', text: 'Keep source citations and leave unknown details unstated.' }], derivation_status: 'ready', scheduled_for: null, derivation_error_code: null, candidate: null, confirmed_candidate_id: null, active: null, job: null };
  } else if (method === 'GET' && path === `${projectPath}/description`) {
    payload = { project_id: projectId, description: state.description, revision: state.descriptionRevision };
  } else if (method === 'PUT' && path === `${projectPath}/description`) {
    const body = await readJSON(req);
    state.description = String(body.description ?? ''); state.descriptionRevision += 1;
    payload = { project_id: projectId, description: state.description, revision: state.descriptionRevision };
  } else if (method === 'GET' && path === `${projectPath}/content-context`) {
    payload = { project_id: projectId, status: 'ready', current_generation: 'fixture-generation-1', current_concepts_sha256: 'f'.repeat(64), stale: false, summary: { text: 'Saved summary from the local fixture.\nRead-only evidence only.', source_generation: 'fixture-generation-1', concepts_sha256: 'f'.repeat(64), generated_at: '2026-10-31T02:00:00Z', prompt_revision: 'fixture-prompt-1', profile_source: 'bootstrap', role: 'heavy', requested_model: 'fixture-model', returned_model: 'fixture-model', input_digest: 'a'.repeat(64), selection_version: 'fixture-selection-1' } };
  } else if (path === `${projectPath}/pipeline/automation`) {
    if (method === 'PUT') {
      const body = await readJSON(req); state.automation = { enabled: Boolean(body.enabled), revision: state.automation.revision + 1 };
    }
    payload = { enabled: state.automation.enabled, revision: state.automation.revision, admin_allowed: false, effective_enabled: false, blocked_reason: 'admin_disabled', backlog_scan_status: 'idle', pipeline_status: 'not_started_disabled', source_revision: 2, processed_revision: 1 };
  } else if (method === 'GET' && path === `${projectPath}/llm-binding`) {
    payload = { source: 'shared', revision: 1 };
  } else if (method === 'PUT' && path === `${projectPath}/llm-binding`) {
    const body = await readJSON(req); payload = { source: body.source ?? 'shared', revision: 2, ...(body.profile_id ? { profile_id: body.profile_id } : {}) };
  } else if (method === 'GET' && path === '/api/v1/llm/profiles') {
    payload = { profiles: [] };
  } else if (method === 'GET' && path === '/api/v1/sources/fixture-source-1-review') {
    payload = { id: 'fixture-source-1', slug: 'fixture-source-1-review', title: 'Review', status: 'published', content: markdown, frontmatter: { source_file: 'review.md' }, annotation_allowed: false };
  } else if (method === 'GET' && path === '/api/v1/auth/google/identity') {
    payload = { primary_email: 'fixture@example.invalid', linked_providers: [] };
  } else if (method === 'GET' && path === '/api/v1/auth/account/password') {
    payload = { allowed: false, configured: false };
  } else if (method === 'GET' && path === '/api/v1/auth/cli/sessions') {
    payload = { sessions: [] };
  } else if (method === 'GET' && path === '/api/v1/auth/cli/bindings') {
    payload = { bindings: [] };
  } else if (method === 'GET' && path === '/api/v1/raw') {
    payload = { files: [{ name: 'review.md', size: Buffer.byteLength(markdown), updated: '2026-10-31T02:00:00Z', sha256: 'a'.repeat(64), ingested: true }, ...(state.finishImport ? [{ name: sourcePath, size: 211, updated: '2026-10-31T02:10:00Z', sha256: 'e'.repeat(64), ingested: true }] : [])] };
  } else if (method === 'GET' && path.startsWith('/api/v1/raw/') && url.searchParams.get('preview') === 'true') {
    payload = path.endsWith('/meeting-notes.md') ? '# Imported fixture\n\nBody.' : markdown; contentType = 'text/markdown; charset=utf-8';
  } else if (method === 'GET' && path === '/api/v1/sources') {
    payload = [{ id: 'fixture-source-1', slug: 'review', title: 'Review', raw_path: 'raw/review.md', lifecycle_status: 'synced', annotation_present: false, annotation_allowed: false }, ...(state.finishImport ? [{ id: 'fixture-source-imported', slug: 'meeting-notes', title: 'meeting-notes', raw_path: sourcePath, lifecycle_status: 'synced', annotation_present: false, annotation_allowed: false }] : [])];
  } else if (method === 'GET' && path === `${projectPath}/document-imports/failure-summary`) {
    payload = { failed_count: state.includeFailure && !state.deletedFailure ? 1 : 0, latest_failure_sequence: state.includeFailure ? 1 : 0, dismissed_through_sequence: 0 };
  } else if (method === 'GET' && path === `${projectPath}/document-imports/failure-reminder`) {
    payload = { failed_count: state.includeFailure && !state.deletedFailure ? 1 : 0, latest_failure_sequence: state.includeFailure ? 1 : 0, dismissed_through_sequence: 0 };
  } else if (method === 'GET' && path === `${projectPath}/document-imports`) {
    const all = [state.imported && importedRecord(), state.includeFailure && !state.deletedFailure && failureRecord()].filter(Boolean);
    const filter = url.searchParams.get('conversion_status') ?? 'all';
    const items = filter === 'all' ? all : filter === 'pending'
      ? all.filter((item) => item.conversion_state === 'converting' || item.source_state === 'saving')
      : all.filter((item) => item.conversion_state === filter);
    payload = { items, snapshot_revision: 1, failed_count: state.includeFailure && !state.deletedFailure ? 1 : 0, latest_failure_sequence: state.includeFailure ? 1 : 0 };
  } else if (method === 'GET' && new RegExp(`^${projectPath}/document-imports/[^/]+$`).test(path)) {
    payload = path.endsWith('fixture-failed-import-1') ? failureRecord() : importedRecord() ?? { error: 'not found' };
    if (!payload.document_id) status = 404;
  } else if (method === 'GET' && path.endsWith('/original')) {
    payload = Buffer.from('Synthetic local fixture document.'); contentType = 'application/vnd.openxmlformats-officedocument.wordprocessingml.document';
  } else if (method === 'DELETE' && path.endsWith('/fixture-failed-import-1')) {
    state.deletedFailure = true; status = 204; payload = null;
  } else if (method === 'POST' && path === '/api/v1/raw/upload') {
    let body = '';
    for await (const chunk of req) body += chunk.toString('latin1');
    const filename = body.match(/filename="([^"]+)"/i)?.[1] ?? 'fixture.docx';
    state.imported = { documentId: 'fixture-import-1', filename, mediaType: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document', bytes: Math.max(1, Math.floor(body.length / 2)) };
    payload = { ...importedRecord(), status_url: `/api/v1/projects/${projectId}/document-imports/${state.imported.documentId}`, site_url: `http://127.0.0.1:4173/sources?document_id=${state.imported.documentId}` };
    status = 202;
  } else if (method === 'GET' && path === `/api/v1/projects/${projectId}/private-check`) {
    status = 403; payload = { error: 'project access denied' };
  } else if (method === 'GET' && path === '/api/v1/pipeline/status') {
    payload = { project_id: projectId, quota: null, suggested_queries: [] };
  } else if (method === 'GET' && path.startsWith('/api/v1/projects/')) {
    status = 404; payload = { error: 'local fixture route not configured' };
  } else {
    status = 404; payload = { error: 'local fixture route not configured' };
  }

  entry.status = status;
  if (Buffer.isBuffer(payload)) {
    res.writeHead(status, { 'Content-Type': contentType, 'Access-Control-Allow-Origin': 'http://127.0.0.1:4173', 'Access-Control-Allow-Headers': 'Authorization, Content-Type, X-Project-ID, If-Match', 'Access-Control-Allow-Methods': 'GET, POST, PUT, DELETE, OPTIONS', 'Cache-Control': 'no-store' });
    return res.end(payload);
  }
  return send(res, status, payload, contentType);
});

server.listen(port, host, () => process.stdout.write(`LWC-454 LOCAL FIXTURE API listening at http://${host}:${port}\n`));
process.on('SIGTERM', () => server.close(() => process.exit(0)));
