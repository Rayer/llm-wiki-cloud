// Synthetic loopback data only. No real identity, credentials or cloud writes.
import { createServer } from 'node:http';

const host = '127.0.0.1';
const port = Number(process.env.LWC459_FIXTURE_PORT ?? 4312);
const projectId = 'fixture-project-1';
const concepts = [
 {id:'c1',slug:'knowledge-lineage',title:'知識的來源與引用',status:'published',description:'沿著來源文件，確認每個知識條目的依據。',date:'2026-10-30'},
 {id:'c2',slug:'project-scope',title:'專案隔離與搜尋範圍',status:'published',description:'以專案組織資料，讓搜尋留在正確的脈絡。',date:'2026-10-29'},
 {id:'c3',slug:'pipeline-review',title:'Pipeline 整理流程',status:'draft',description:'從原始文件到編譯條目，保留可以追溯的步驟。'},
 {id:'c4',slug:'source-notes',title:'來源註記的使用方式',status:'published',description:'讓補充註記與原始文件共同提供整理脈絡。',date:'2026-10-27'},
];
const sources = [
 {id:'s1',slug:'research-notes',title:'研究筆記與引用規範',raw_path:'raw/research-notes.md',lifecycle_status:'synced',annotation_present:true,annotation_allowed:true},
 {id:'s2',slug:'project-handbook',title:'專案知識手冊',raw_path:'raw/project-handbook.md',lifecycle_status:'synced',annotation_present:false,annotation_allowed:true},
 {id:'s3',slug:'pipeline-notes',title:'Pipeline 整理筆記',raw_path:'raw/pipeline-notes.md',lifecycle_status:'notes_pending',annotation_present:true,annotation_allowed:true},
 {id:'s4',slug:'review-notes',title:'待整理的閱讀資料',raw_path:'raw/review-notes.md',lifecycle_status:'new',annotation_present:false,annotation_allowed:true},
];
const sourcePath = 'raw/meeting-notes.md';
const markdown = `# Synthetic source heading\n\nA paragraph directly before a table.\n\n| Topic | Detail |\n| --- | --- |\n| Status | Synthetic local fixture |\n\n## Notes\n\n- Preserve the source link\n- Keep unknowns explicit\n\n\`\`\`text\nfixture only\n\`\`\``;
let sessionSequence = 0;
const state = {
  bootstrap: 'ready', authenticated: true,
  imported: null,
  finishImport: false,
  includeFailure: false,
  deletedFailure: false,
  description: '本機合成資料：把分散的研究資料整理成可搜尋、可追溯的知識。',
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
    'Access-Control-Allow-Origin': 'http://127.0.0.1:3001',
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
      for (const key of ['bootstrap', 'finishImport', 'includeFailure', 'authenticated']) {
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
  const selectedProject = req.headers['x-project-id'] ?? url.searchParams.get('project_id') ?? projectId;
  const projectPath = `/api/v1/projects/${projectId}`;
  const scopedConcepts = selectedProject === 'fixture-project-2' ? [{id:'c5',slug:'separate-project',title:'獨立專案的知識',status:'draft',description:'此條目只屬於第二個合成專案。'}] : concepts;
  const scopedSources = selectedProject === 'fixture-project-2' ? [{...sources[0],id:'s5',slug:'separate-source',title:'獨立專案來源',raw_path:'raw/separate.md'}] : sources;
  let status = 200;
  let payload = {};
  let contentType = 'application/json; charset=utf-8';

  if (path === '/frontend-config.json') {
    payload = {schema_version:1,api_url:`http://${host}:${port}`,auth_url:`http://${host}:${port}`};
  } else if (path === '/api/v1/auth/refresh' || path === '/api/v1/auth/login') {
    if (path.endsWith('/login')) state.authenticated = true;
    if (!state.authenticated) {status=401;payload={error:'Synthetic signed-out session'};}
    else payload = {access_token:`lwc459-synthetic-local-${++sessionSequence}`,user:{id:'fixture-owner',email:'synthetic@example.invalid'}};
  } else if (path === '/api/v1/public/config') {
    payload = {registration_enabled:true,email_registration_enabled:true,demo_enabled:true,announcement_markdown:'# 本機畫面驗證\n\n所有帳號、專案與文件都是合成資料。',announcement_digest:'sha256:'+'a'.repeat(64)};
  } else if (method === 'POST' && path === '/api/v1/raw/scrape') {
    payload = {status:'preview',message:'本機合成預覽，尚未儲存任何原文。',candidate_id:'synthetic-url-candidate',candidate_revision:'synthetic-url-revision',expires_at:'2099-01-01T00:00:00Z',preview:{title:'網址來源預覽（合成資料）',source_url:'https://synthetic.invalid/article',markdown:'# 網址來源預覽\n\n此為本機 fixture 的合成內容，未連線抓取外部網站。\n\n## 可追溯的閱讀\n\n確認標題與內容後，才決定是否儲存。',candidate_sha256:'f'.repeat(64),provider:'local-synthetic-fixture'}};
  } else if (path === '/api/v1/query') {
    payload = {answer:'此為本機合成回答。知識可以沿著引用回到來源 [研究筆記與引用規範]。',results:scopedConcepts.slice(0,2),citations:[{index:1,id:'s1',type:'source',slug:'research-notes',title:'研究筆記與引用規範',path:'sources/s1-research-notes.md'}]};
  } else if (method === 'GET' && /^\/api\/v1\/(concepts|sources)\/.+/.test(path)) {
    const isSource = path.includes('/sources/');
    const list = isSource ? scopedSources : scopedConcepts;
    const key = decodeURIComponent(path.split('/').at(-1));
    const item = list.find(item=>key === item.slug || key === `${item.id}-${item.slug}`) ?? list[0];
    payload = {...item,status:'published',content:`# ${item.title}\n\n此為 LWC-459 本機合成資料，供畫面與操作驗證。\n\n## 可追溯的脈絡\n\n沿著來源閱讀，保留資料與解讀的界線。\n\n[回到來源](/sources/s1-research-notes)`,frontmatter:{source_file:'research-notes.md'},annotation_allowed:false};
  } else if (method === 'POST' && path === '/api/v1/onboarding/default-project') {
    if (state.bootstrap === 'unavailable') { status = 503; payload = { error: 'account access unavailable' }; }
    else if (state.bootstrap === 'unauthorized') { status = 401; payload = { error: 'invalid token' }; }
    else payload = { schema_version: 1, status: 'ready', default_project_id: projectId };
  } else if (method === 'GET' && path === '/api/v1/projects') {
    payload = [{ id: projectId, name: '知識工作室（合成資料）', description: state.description, description_revision: state.descriptionRevision }, {id:'fixture-project-2',name:'第二專案（合成資料）',description:'專案隔離驗證',description_revision:1}];
  } else if (method === 'GET' && path === '/api/v1/status') {
    payload = {sources_count:scopedSources.length,concepts_count:scopedConcepts.length,raw_count:scopedSources.length,suggested_queries:['知識如何回到來源？','如何組織專案脈絡？']};
  } else if (method === 'GET' && path === '/api/v1/concepts') {
    payload = scopedConcepts;
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
  } else if (method === 'GET' && (path === '/api/v1/mcp/oauth/grants' || path === '/api/v1/auth/mcp/oauth/grants')) {
    payload = {grants:[]};
  } else if (method === 'POST' && path.startsWith('/api/v1/auth/mcp/oauth/consent/')) {
    payload = {requires_consent:true,redirect_url:`http://${host}:3001/advanced`};
  } else if (method === 'GET' && path.startsWith('/api/v1/auth/mcp/oauth/consent/')) {
    payload = {schema_version:1,status:'pending',transaction_id:'synthetic-consent',requires_consent:true,account_email:'synthetic@example.invalid',project_id:projectId,project_name:'知識工作室（合成資料）',client_name:'本機驗證用 Agent',resource:`http://${host}:${port}/mcp/projects/${projectId}`,resource_kind:'project',scope:'mcp:query mcp:raw:write',expires_at:'2099-01-01T00:00:00Z'};
  } else if (method === 'GET' && path === '/api/v1/llm/profiles') {
    payload = { profiles: [] };
  } else if (method === 'GET' && path === '/api/v1/sources/fixture-source-1-review') {
    payload = { id: 'fixture-source-1', slug: 'fixture-source-1-review', title: 'Review', status: 'published', content: markdown, frontmatter: { source_file: 'review.md' }, annotation_allowed: false };
  } else if (method === 'GET' && path === '/api/v1/auth/google/identity') {
    payload = { primary_email: 'fixture@example.invalid', linked_providers: [] };
  } else if (method === 'GET' && path === '/api/v1/auth/account/password') {
    payload = { allowed: true, configured: true };
  } else if (method === 'GET' && path === '/api/v1/auth/cli/sessions') {
    payload = {sessions:[{id:'synthetic-cli-session',client_name:'本機 CLI（合成資料）',status:'active',created_at:'2026-10-30T02:00:00Z',updated_at:'2026-10-31T02:00:00Z'}]};
  } else if (method === 'GET' && path === '/api/v1/auth/cli/bindings') {
    payload = {bindings:[{binding_id:'synthetic-binding',host:'local-fixture',wiki_id:'synthetic-wiki',project_id:projectId,authorized_by:'fixture-owner',status:'active',created_at:'2026-10-30T02:00:00Z',updated_at:'2026-10-31T02:00:00Z'}]};
  } else if (method === 'GET' && path === '/api/v1/raw') {
    payload = { files: [{ name: 'review.md', size: Buffer.byteLength(markdown), updated: '2026-10-31T02:00:00Z', sha256: 'a'.repeat(64), ingested: true }, ...(state.finishImport ? [{ name: sourcePath, size: 211, updated: '2026-10-31T02:10:00Z', sha256: 'e'.repeat(64), ingested: true }] : [])] };
  } else if (method === 'GET' && path.startsWith('/api/v1/raw/') && url.searchParams.get('preview') === 'true') {
    payload = path.endsWith('/meeting-notes.md') ? '# Imported fixture\n\nBody.' : markdown; contentType = 'text/markdown; charset=utf-8';
  } else if (method === 'GET' && path === '/api/v1/sources') {
    payload = scopedSources;
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
    payload = { ...importedRecord(), status_url: `/api/v1/projects/${projectId}/document-imports/${state.imported.documentId}`, site_url: `http://127.0.0.1:3001/sources?document_id=${state.imported.documentId}` };
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
    res.writeHead(status, { 'Content-Type': contentType, 'Access-Control-Allow-Origin': 'http://127.0.0.1:3001', 'Access-Control-Allow-Headers': 'Authorization, Content-Type, X-Project-ID, If-Match', 'Access-Control-Allow-Methods': 'GET, POST, PUT, DELETE, OPTIONS', 'Cache-Control': 'no-store' });
    return res.end(payload);
  }
  return send(res, status, payload, contentType);
});

server.listen(port, host, () => process.stdout.write(`LWC-459 SYNTHETIC LOCAL FIXTURE API listening at http://${host}:${port}\n`));
process.on('SIGTERM', () => server.close(() => process.exit(0)));
