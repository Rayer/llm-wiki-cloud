'use client';

import { isValidRawRelativePath } from './raw-file-name.ts';
import { getRuntimeConfig } from './runtime-config.ts';
import { normalizeAnnotationBody, normalizeAnnotationGeneration } from './source-annotation.ts';
import { recordRequestIDHeader, recordRequestResponse } from './request-ids.ts';

export type PipelineDiagnostic = {
	stage?: string | null;
	error_class?: string | null;
	detail_code?: string | null;
	child_command?: string | null;
	exit_code?: number | null;
	message?: string | null;
};

export type PipelineExecution = {
  name?: string | null;
  status?: 'RUNNING' | 'SUCCEEDED' | 'FAILED' | string;
  compile_mode?: 'incremental' | 'full' | string;
  publication_outcome?: 'confirmed_published' | 'confirmed_not_published' | 'unknown' | string;
  consumed_guidance?: {
    status: 'available' | 'unavailable' | string;
    execution_id?: string;
    content_generation?: string;
    kind?: 'none' | 'active' | 'bootstrap' | string;
    revision: string | null;
    saved_profile_revision?: number;
    saved_profile_input_digest?: string;
    source_profile_revision?: number;
    source_profile_input_digest?: string;
    source_model_version?: string;
    source_prompt_version?: string;
    source_schema_version?: string;
    source_candidate_id?: string;
    source_content_generation?: string;
    source_status?: string;
  } | null;
  duration?: string | number | null;
  started_at?: string | null;
  finished_at?: string | null;
  log_url?: string | null;
  log_state?: 'pending' | 'available' | 'unavailable' | 'missing' | string | null;
	log_state_reason?: string | null;
	diagnostic?: PipelineDiagnostic | null;
	diagnostic_state?: 'available' | 'unavailable' | string | null;
	failure_reason?: string | null;
	quota_settlement?: 'pending' | 'refunded' | 'charged' | 'not_applicable' | 'unknown' | string | null;
	[key: string]: unknown;
};

export type ApiStatus = {
  sourcesCount: number;
  conceptsCount: number;
  rawCount: number;
  suggestedQueries: string[];
	lastExecution?: PipelineExecution | null;
	scheduledExecution?: PipelineExecution | null;
	quota?: PipelineQuota | null;
	scheduledPipeline?: ScheduledPipelineStatus | null;
	raw: Record<string, unknown>;
};

export type LLMProfile = {
  id: string;
  name: string;
  provider: 'openai' | 'xai' | 'anthropic' | 'custom';
  protocol: 'openai_chat_completions' | 'anthropic_messages';
  endpoint: string;
  model: string;
  revision: number;
  secret_configured: boolean;
  credential_origin?: 'managed' | 'deployment';
  credential_reference?: string;
  enabled?: boolean;
  available_to_public?: boolean;
  priority?: number;
};

export type ProjectLLMBinding = {
  source: 'shared' | 'personal';
  profile_id?: string;
  profile_revision?: number;
  revision: number;
};

export type LLMProfileInput = {
  name: string;
  provider: LLMProfile['provider'];
  protocol: LLMProfile['protocol'];
  endpoint: string;
  model: string;
  api_key?: string;
  credential_reference?: string;
  clear_api_key?: boolean;
  revision?: number;
  enabled?: boolean;
  available_to_public?: boolean;
  priority?: number;
};

export type WikiEntry = {
  slug: string;
  title: string;
  id?: string;
  status?: string;
  description?: string;
  date?: string;
  frontmatter?: Record<string, unknown>;
  content?: string;
  annotationAllowed?: boolean;
  raw: unknown;
};

export type SourceLifecycle = 'new' | 'synced' | 'notes_pending' | 'content_pending' | 'error';

export type SourceListItem = {
  id?: string;
  slug: string;
  title: string;
  rawPath: string;
  lifecycle: SourceLifecycle;
  annotationPresent: boolean;
  annotationAllowed: boolean;
  error?: string;
  raw: unknown;
};

export type SourceAnnotation = {
  body: string;
  expectedGeneration: string;
  hasAnnotation: boolean;
  annSha256?: string;
  annotationDirty: boolean;
  rawDirty: boolean;
  dirty: boolean;
  lifecycleStatus?: string;
  updatedAt?: string;
  updatedBy?: string;
};

export type RawFile = {
  name: string;
  size: number;
  updated: string;
  sha256: string;
  ingested: boolean;
  raw: unknown;
};

export type SearchResult = WikiEntry & {
  excerpt?: string;
  score?: number;
  type?: string;
};

export type Citation = {
  text: string;
  slug?: string;
  type: 'concept' | 'source';
  path?: string;
  id?: string;
};

export type QueryExpansion = {
  keywords: string[];
};

export type QueryTerminalMetadata = {
  status?: string;
  reason?: string;
  answer_basis?: string;
  wiki_evidence_status?: string;
  disclosure_required?: boolean;
  ai_operation_failures?: QueryOperationFailure[];
};

export type QueryOperationFailure = {
  stage: 'query_expansion' | 'answer_synthesis';
  source?: 'personal' | 'shared' | 'unknown';
  class:
    | 'authentication'
    | 'rate_limited'
    | 'quota_exhausted'
    | 'billing'
    | 'timeout'
    | 'invalid_response'
    | 'output_truncated'
    | 'provider_unavailable'
    | 'provider_error'
    | 'context_unavailable'
    | 'canceled';
  fallback: 'none' | 'raw_query' | 'deterministic_plan' | 'retrieval_only';
};

export type SearchResponse = QueryTerminalMetadata & {
  results: SearchResult[];
  aiAnswer: string;
  citations: Citation[];
  expand?: QueryExpansion;
};

export type AdminProject = {
  id: string;
  name: string;
  /** Storage-scoped project id (not the Firestore compound doc id). */
  projectId: string;
  userId: string;
  userName: string;
  userEmail: string;
  conceptCount: number;
  sourceCount: number;
};

export type AdminUser = {
  status: string;
  id: string;
  name: string;
  email: string;
  scopeStates: Partial<Record<'auth:admin' | 'lwc:admin', { state: string; revision: number; source: string }>>;
  projectCount: number;
};

export type AdminManagementScope = {
  id: 'auth:admin' | 'lwc:admin';
  product: string;
  resource: string;
  grant_mode: 'operator-only';
  display_name: { en: string; 'zh-TW': string };
  description: { en: string; 'zh-TW': string };
};

export type BuildInfo = {
  product_version: string;
  commit: string;
  branch: string;
  tag: string;
  image_tag: string;
  service: string;
  revision: string;
};

export class ApiError extends Error {
  status: number;
  diagnosticId?: string;
  requestId?: string;

  constructor(message: string, status: number, diagnosticId?: string, requestId?: string | null) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.diagnosticId = normalizeDiagnosticId(diagnosticId);
    this.requestId = normalizeRequestId(requestId);
  }
}

export class QueryApiError extends ApiError {
  code: string;

  constructor(message: string, status: number, code: string, diagnosticId?: string, requestId?: string | null) {
    super(message, status, diagnosticId, requestId);
    this.name = 'QueryApiError';
    this.code = code;
  }
}

function normalizeDiagnosticId(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined;
  const normalized = value.trim();
  return /^[A-Za-z0-9_-]{8,100}$/.test(normalized) ? normalized : undefined;
}

export function normalizeRequestId(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined;
  const normalized = value.trim();
  return /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i.test(normalized)
    ? normalized
    : undefined;
}

export function diagnosticIdFromPayload(payload: unknown): string | undefined {
  return isRecord(payload) ? normalizeDiagnosticId(payload.diagnostic_id) : undefined;
}

export type ProfileRequirement = { id: string; text: string };
export type ProfileDerivedRef = {
  revision: string;
  input_digest: string;
  model_version: string;
  prompt_version: string;
  schema_version: string;
};
export type ProfileRequirementAccounting = {
  id: string;
  disposition: 'compile_guidance' | 'dictionary_or_query' | 'both' | 'limitation';
  explanation: string;
};
export type ProfileCandidate = {
  candidate_id: string;
  source: 'manual' | 'compile_auto';
  base_revision: number;
  requirements_digest: string;
  content_generation: string;
  dictionary: ProfileDerivedRef;
  guidance: ProfileDerivedRef;
  preview: { dictionary_diff: string; guidance_diff: string; requirements: ProfileRequirementAccounting[] };
};
export type ProfileJob = {
  job_id: string;
  candidate_id: string;
  content_generation: string;
  status: 'scheduled' | 'running' | 'retry_wait' | 'incomplete' | 'ready' | 'superseded';
  missing_count: number;
  error_code: string | null;
};
export type ProfileActive = {
  candidate_id: string;
  content_generation: string;
  dictionary_revision: string;
  tag_set_revision: string;
  query_rule_revision: string;
  guidance_revision: string;
};
export type ProfileState = {
  project_id: string;
  revision: number;
  requirements: ProfileRequirement[];
  derivation_status: 'pending' | 'ready' | 'failed' | 'superseded' | null;
  scheduled_for: string | null;
  derivation_error_code: string | null;
  candidate: ProfileCandidate | null;
  confirmed_candidate_id: string | null;
  active: ProfileActive | null;
  job: ProfileJob | null;
};
export type ProfileBootstrapRequirement = ProfileRequirementAccounting;
export type ProfileBootstrapGuidance = {
  revision: string;
  input_digest: string;
  profile_revision: number;
  status: 'preview_ready' | 'confirmed';
  model_version: string;
  prompt_version: string;
  schema_version: string;
  confirmed_at: string | null;
  preview: {
    guidance_diff: string;
    requirements: ProfileBootstrapRequirement[];
  };
};
export type ProfileBootstrapGuidanceResponse = {
  bootstrap_guidance: ProfileBootstrapGuidance | null;
};
export type ProfileGuidanceArtifact = {
  revision: string;
  input_digest: string;
  model_version: string;
  prompt_version: string;
  schema_version: string;
  compile_guidance: string;
};
export type ProfileGuidanceArtifactResponse = {
  guidance_artifact: ProfileGuidanceArtifact;
};
export type RecompileAllCapability = {
  allowed: boolean;
  compile_mode: 'full';
  denial_code?: string;
  quota: PipelineQuota | null;
};

const LAST_PROJECT_KEY = 'llm-wiki-last-project';

type ApiAuthConfig = {
  getAccessToken: () => string | null;
  getSessionEpoch?: () => number;
  refreshAccessToken: () => Promise<ApiRefreshResult | null>;
  onUnauthorized: (failedToken: string, failedEpoch: number) => void;
};

export type ApiRefreshResult = {
  accessToken: string;
  epoch: number;
};

let apiAuthConfig: ApiAuthConfig = {
  getAccessToken: () => null,
  refreshAccessToken: async () => null,
  onUnauthorized: () => undefined,
};

export function configureApiAuth(config: ApiAuthConfig): void {
  apiAuthConfig = config;
}

export function toV1Path(path: string): string {
  if (path.startsWith('/api/v1/')) return path;
  return path.startsWith('/api/') ? `/api/v1/${path.slice('/api/'.length)}` : path;
}

export function buildProjectHeaders(
  projectId: string,
  accessToken: string,
  json = false,
): Record<string, string> {
  return {
    Authorization: `Bearer ${accessToken}`,
    ...(json ? { 'Content-Type': 'application/json' } : {}),
    'X-Project-ID': projectId,
  };
}

type BuildRequestInitOptions = {
  method?: string;
  projectId?: string;
  accessToken?: string | null;
  json?: boolean;
  body?: BodyInit;
  headers?: HeadersInit;
  signal?: AbortSignal;
};

export function buildRequestInit({
  method,
  projectId,
  accessToken,
  json = false,
  body,
  headers: extraHeaders,
  signal,
}: BuildRequestInitOptions): RequestInit {
  const headers: Record<string, string> = {
    ...(json ? { 'Content-Type': 'application/json' } : {}),
    ...(extraHeaders ? Object.fromEntries(new Headers(extraHeaders).entries()) : {}),
  };

  if (accessToken) {
    headers.Authorization = `Bearer ${accessToken}`;
  }

  if (projectId) {
    headers['X-Project-ID'] = projectId;
  }

  return {
    ...(method ? { method } : {}),
    credentials: 'include',
    headers,
    ...(signal === undefined ? {} : { signal }),
    ...(body === undefined ? {} : { body }),
  };
}

function selectedProjectId(): string {
  const projectId = window.localStorage.getItem(LAST_PROJECT_KEY);
  if (!projectId) throw new Error('Please select a project first');
  return projectId;
}

type ApiAuthOperation = {
  auth: ApiAuthConfig;
  accessToken: string;
  epoch: number;
};

async function accessTokenOrRefresh(): Promise<ApiAuthOperation> {
  const auth = apiAuthConfig;
  const epoch = auth.getSessionEpoch?.() ?? 0;
  const current = auth.getAccessToken();
  if (current) return { auth, accessToken: current, epoch };

  const refreshed = asRefreshResult(await auth.refreshAccessToken());
  if (
    refreshed
    && apiAuthConfig === auth
    && (auth.getSessionEpoch?.() ?? refreshed.epoch) === refreshed.epoch
  ) {
    return {
      auth,
      accessToken: refreshed.accessToken,
      epoch: refreshed.epoch,
    };
  }

  throw new Error('Authentication required');
}

type ApiFetchOptions = {
  method?: string;
  projectId?: string;
  json?: boolean;
  body?: BodyInit;
  headers?: HeadersInit;
  requireProject?: boolean;
  signal?: AbortSignal;
  onRequestDispatched?: () => void;
  onResponseReceived?: (response: Response) => void;
};

export async function apiFetch(path: string, options: ApiFetchOptions = {}): Promise<Response> {
  const projectId = options.requireProject === false
    ? options.projectId
    : options.projectId ?? selectedProjectId();
  const { auth, accessToken, epoch: requestEpoch } = await accessTokenOrRefresh();
  const url = `${getRuntimeConfig().api_url}${toV1Path(path)}`;
  const init = buildRequestInit({ ...options, projectId, accessToken });
  options.onRequestDispatched?.();
  const response = recordRequestResponse(await fetch(url, init));
  options.onResponseReceived?.(response);

  if (response.status !== 401) return response;
  if (apiAuthConfig !== auth || (auth.getSessionEpoch?.() ?? 0) !== requestEpoch) return response;

  const refreshed = await auth.refreshAccessToken();
  const refreshedOperation = asRefreshResult(refreshed);
  if (apiAuthConfig !== auth) return response;
  if (refreshed === null) {
    if ((auth.getSessionEpoch?.() ?? 0) !== requestEpoch) return response;
    auth.onUnauthorized(accessToken, requestEpoch);
    return response;
  }
  if (refreshedOperation === null) return response;
  if ((auth.getSessionEpoch?.() ?? 0) !== refreshedOperation.epoch) return response;

  options.onRequestDispatched?.();
  const retriedResponse = recordRequestResponse(await fetch(url, buildRequestInit({
    ...options,
    projectId,
    accessToken: refreshedOperation.accessToken,
  })));
  options.onResponseReceived?.(retriedResponse);
  if (
    retriedResponse.status === 401
    && apiAuthConfig === auth
    && (auth.getSessionEpoch?.() ?? 0) === refreshedOperation.epoch
  ) {
    auth.onUnauthorized(refreshedOperation.accessToken, refreshedOperation.epoch);
  }
  return retriedResponse;
}

async function llmProfileRequest<T>(path: string, options: { method?: string; projectId?: string; body?: unknown } = {}): Promise<T> {
  const hasBody = options.body !== undefined;
  const response = await apiFetch(path, {
    method: options.method,
    projectId: options.projectId,
    requireProject: options.projectId !== undefined ? true : false,
    json: hasBody,
    body: hasBody ? JSON.stringify(options.body) : undefined,
  });
  const payload: unknown = response.status === 204 ? null : await response.json().catch(() => null);
  if (!response.ok) {
    const message = isRecord(payload) ? asString(payload.error) ?? asString(payload.message) : undefined;
    throw new ApiError(message ?? `LLM profile request failed (${response.status})`, response.status, undefined, response.headers.get('X-Request-ID'));
  }
  return payload as T;
}

export async function listLLMProfiles(): Promise<LLMProfile[]> {
  const payload = await llmProfileRequest<{ profiles?: LLMProfile[] }>('/api/v1/llm/profiles');
  return Array.isArray(payload.profiles) ? payload.profiles : [];
}

export async function createLLMProfile(input: LLMProfileInput): Promise<LLMProfile> {
  return llmProfileRequest<LLMProfile>('/api/v1/llm/profiles', { method: 'POST', body: input });
}

export async function updateLLMProfile(profileId: string, input: LLMProfileInput): Promise<LLMProfile> {
  return llmProfileRequest<LLMProfile>(`/api/v1/llm/profiles/${encodeURIComponent(profileId)}`, { method: 'PUT', body: input });
}

export async function deleteLLMProfile(profileId: string): Promise<void> {
  await llmProfileRequest<void>(`/api/v1/llm/profiles/${encodeURIComponent(profileId)}`, { method: 'DELETE' });
}

export type DeploymentLLMCredentialReference = { reference: string; label: string };
export type AdminLLMProfiles = {
  profiles: LLMProfile[];
  deployment_credentials: DeploymentLLMCredentialReference[];
};

export async function listAdminLLMProfiles(): Promise<AdminLLMProfiles> {
  const payload = await llmProfileRequest<{ profiles?: LLMProfile[]; deployment_credentials?: DeploymentLLMCredentialReference[] }>('/api/v1/admin/llm/profiles');
  return {
    profiles: Array.isArray(payload.profiles) ? payload.profiles : [],
    deployment_credentials: Array.isArray(payload.deployment_credentials) ? payload.deployment_credentials : [],
  };
}

export async function createAdminLLMProfile(input: LLMProfileInput): Promise<LLMProfile> {
  return llmProfileRequest<LLMProfile>('/api/v1/admin/llm/profiles', { method: 'POST', body: input });
}

export async function updateAdminLLMProfile(profileId: string, input: LLMProfileInput): Promise<LLMProfile> {
  return llmProfileRequest<LLMProfile>(`/api/v1/admin/llm/profiles/${encodeURIComponent(profileId)}`, { method: 'PUT', body: input });
}

export async function deleteAdminLLMProfile(profileId: string): Promise<void> {
  await llmProfileRequest<void>(`/api/v1/admin/llm/profiles/${encodeURIComponent(profileId)}`, { method: 'DELETE' });
}

export async function getProjectLLMBinding(projectId: string): Promise<ProjectLLMBinding> {
  const encoded = encodeURIComponent(projectId);
  return llmProfileRequest<ProjectLLMBinding>(`/api/v1/projects/${encoded}/llm-binding`, { projectId });
}

export async function saveProjectLLMBinding(projectId: string, binding: Omit<ProjectLLMBinding, 'profile_revision'>): Promise<ProjectLLMBinding> {
  const encoded = encodeURIComponent(projectId);
  return llmProfileRequest<ProjectLLMBinding>(`/api/v1/projects/${encoded}/llm-binding`, { method: 'PUT', projectId, body: binding });
}

async function requestJson<T>(path: string, options: Pick<ApiFetchOptions, 'projectId'> = {}): Promise<T> {
  const response = await apiFetch(path, options);

  if (!response.ok) {
    const payload: unknown = await response.json().catch(() => null);
    throw new ApiError(
      `API request failed (${response.status})`,
      response.status,
      diagnosticIdFromPayload(payload),
      response.headers.get('X-Request-ID'),
    );
  }

  return response.json() as Promise<T>;
}

async function postJson<T>(path: string, body: unknown): Promise<T> {
  const response = await apiFetch(path, {
    method: 'POST',
    json: true,
    body: JSON.stringify(body),
  });

  if (!response.ok) {
    const payload: unknown = await response.json().catch(() => null);
    const detail = isRecord(payload)
      ? [payload.detail, payload.error, payload.message]
        .find((value) => typeof value === 'string' && value.trim())
      : undefined;
    const rawCode = isRecord(payload) ? asString(payload.code)?.trim() : undefined;
    const code = rawCode && /^[a-z][a-z0-9_]{0,63}$/.test(rawCode) ? rawCode : '';
    const diagnosticId = diagnosticIdFromPayload(payload);
    const message = typeof detail === 'string' ? detail.trim() : '';
    throw new QueryApiError(
      message || 'The query could not be completed. Please try again.',
      response.status,
      code,
      diagnosticId,
      response.headers.get('X-Request-ID'),
    );
  }

  return response.json() as Promise<T>;
}

function profilePathSegment(value: string): string {
  let decoded: string;
  try {
    decoded = decodeURIComponent(value);
  } catch {
    throw new Error('Invalid project ID.');
  }
  if (!safeWikiRouteSegment(value) || !safeWikiRouteSegment(decoded)) {
    throw new Error('Invalid project ID.');
  }
  return encodeURIComponent(value);
}

function profileApiError(payload: unknown, fallback: string): string {
  if (!isRecord(payload)) return fallback;
  return asString(payload.error) ?? asString(payload.message) ?? asString(payload.detail) ?? fallback;
}

async function profileRequest<T>(projectId: string, suffix = '', options: {
  method?: string;
  body?: unknown;
} = {}): Promise<T> {
  const safeProjectId = profilePathSegment(projectId);
  const hasBody = options.body !== undefined;
  const response = await apiFetch(`/api/v1/projects/${safeProjectId}/profile${suffix}`, {
    method: options.method,
    projectId,
    json: hasBody,
    body: hasBody ? JSON.stringify(options.body) : undefined,
  });
  const payload: unknown = await response.json().catch(() => null);
  if (!response.ok) {
    throw new ApiError(profileApiError(payload, `Profile request failed (${response.status})`), response.status, undefined, response.headers.get('X-Request-ID'));
  }
  return payload as T;
}

export async function getProfile(projectId: string): Promise<ProfileState> {
  return profileRequest<ProfileState>(projectId);
}

export async function getProfileBootstrapGuidance(projectId: string): Promise<ProfileBootstrapGuidanceResponse> {
  return profileRequest<ProfileBootstrapGuidanceResponse>(projectId, '/bootstrap-guidance');
}

export async function getProfileGuidanceArtifact(projectId: string, revision: string): Promise<ProfileGuidanceArtifactResponse> {
  return profileRequest<ProfileGuidanceArtifactResponse>(projectId, `/guidance/${profilePathSegment(revision)}`);
}

export async function confirmProfileBootstrapGuidance(
  projectId: string,
  revision: string,
  expectedRevision: number,
  inputDigest: string,
): Promise<ProfileBootstrapGuidanceResponse> {
  return profileRequest<ProfileBootstrapGuidanceResponse>(projectId, `/bootstrap-guidance/${profilePathSegment(revision)}/confirm`, {
    method: 'POST',
    body: { expected_revision: expectedRevision, input_digest: inputDigest },
  });
}

export async function saveProfile(
  projectId: string,
  expectedRevision: number,
  requirements: ProfileRequirement[],
): Promise<ProfileState> {
  return profileRequest<ProfileState>(projectId, '', {
    method: 'PUT',
    body: { expected_revision: expectedRevision, requirements },
  });
}

export async function confirmProfileCandidate(
  projectId: string,
  candidateId: string,
  expectedRevision: number,
): Promise<ProfileState> {
  return profileRequest<ProfileState>(projectId, `/candidates/${profilePathSegment(candidateId)}/confirm`, {
    method: 'POST',
    body: { expected_revision: expectedRevision },
  });
}

export async function retryProfileCandidate(
  projectId: string,
  candidateId: string,
  expectedRevision: number,
): Promise<ProfileState> {
  return profileRequest<ProfileState>(projectId, `/candidates/${profilePathSegment(candidateId)}/retry`, {
    method: 'POST',
    body: { expected_revision: expectedRevision },
  });
}

export async function retryProfileDerivation(
  projectId: string,
  expectedRevision: number,
): Promise<ProfileState> {
  return profileRequest<ProfileState>(projectId, '/derivation/retry', {
    method: 'POST',
    body: { expected_revision: expectedRevision },
  });
}

export async function getProfileJob(projectId: string, jobId: string): Promise<ProfileJob> {
  return profileRequest<ProfileJob>(projectId, `/jobs/${profilePathSegment(jobId)}`);
}

export async function getRecompileAllCapability(projectId: string): Promise<RecompileAllCapability> {
  const projectSegment = profilePathSegment(projectId);
  const response = await apiFetch(`/api/v1/projects/${projectSegment}/recompile-all/capability`, { projectId });
  const payload: unknown = await response.json().catch(() => null);
  if (!response.ok) {
    throw new ApiError(profileApiError(payload, `Recompile capability request failed (${response.status})`), response.status, undefined, response.headers.get('X-Request-ID'));
  }
  if (!isRecord(payload) || typeof payload.allowed !== 'boolean' || payload.compile_mode !== 'full') {
    throw new ApiError('Invalid recompile capability response', 502);
  }
  const quota = payload.quota == null ? null : normalizePipelineQuota(payload.quota);
  if (!quota && payload.denial_code !== 'unsupported_mode') {
    throw new ApiError('Invalid recompile capability quota', 502);
  }
  return {
    allowed: payload.allowed,
    compile_mode: 'full',
    ...(asString(payload.denial_code) ? { denial_code: asString(payload.denial_code) } : {}),
    quota,
  };
}

export async function recompileAll(projectId: string): Promise<PipelineResult> {
  const projectSegment = profilePathSegment(projectId);
  const response = await apiFetch(`/api/v1/projects/${projectSegment}/recompile-all`, {
    method: 'POST',
    projectId,
  });
  if (!response.ok) {
    const payload: unknown = await response.json().catch(() => null);
    throw new ApiError(profileApiError(payload, `Recompile all failed (${response.status})`), response.status, undefined, response.headers.get('X-Request-ID'));
  }
  const payload: unknown = await response.json().catch(() => null);
  if (
    !isRecord(payload)
    || payload.status !== 'accepted'
    || payload.compile_mode !== 'full'
    || payload.project_id !== projectId
    || (!asString(payload.execution_id) && !asString(payload.intent_id))
  ) {
    throw new ApiError('Invalid recompile response', 502);
  }
  return {
    status: 'accepted',
    compile_mode: 'full',
    ...(asString(payload.execution_id) ? { execution_id: asString(payload.execution_id) } : {}),
    ...(asString(payload.intent_id) ? { intent_id: asString(payload.intent_id) } : {}),
    ...(asString(payload.intent_status) ? { intent_status: asString(payload.intent_status) } : {}),
    project_id: projectId,
    ...(payload.quota == null ? {} : { quota: normalizePipelineQuota(payload.quota) }),
  };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

function asString(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value : undefined;
}

function asNumber(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined;
}

function asRefreshResult(value: unknown): ApiRefreshResult | null {
  const record = isRecord(value) ? value : null;
  if (!record) return null;

  const accessToken = asString(record.accessToken);
  const epoch = asNumber(record.epoch);
  if (!accessToken || epoch === undefined) return null;

  return { accessToken, epoch };
}

export function citationPathSegment(type: 'concept' | 'source', path: unknown): string | null {
  if (typeof path !== 'string') return null;

  const match = path.match(/^\/(concepts|sources)\/([^/?#]+)(?:[?#].*)?$/);
  if (!match) return null;

  if (
    (type === 'concept' && match[1] !== 'concepts')
    || (type === 'source' && match[1] !== 'sources')
  ) {
    return null;
  }

  const segment = match[2];
  if (!segment) return null;

  try {
    const decoded = decodeURIComponent(segment);
    return safeWikiRouteSegment(decoded);
  } catch {
    return null;
  }
}

export function safeWikiRouteSegment(value: unknown): string | null {
  if (typeof value !== 'string' || !value || value !== value.trim()) return null;
  if (
    value === '.'
    || value === '..'
    || value.includes('/')
    || value.includes('\\')
    || /[\u0000-\u001f\u007f]/.test(value)
  ) {
    return null;
  }
  return value;
}

function asExitCode(value: unknown): number | null {
  return typeof value === 'number' && Number.isInteger(value) && value >= 0 && value <= 255
    ? value
    : null;
}

function normalizePipelineDiagnostic(value: unknown): PipelineDiagnostic | null {
  if (!isRecord(value)) return null;
  return {
    stage: asString(value.stage) ?? null,
    error_class: asString(value.error_class) ?? null,
    detail_code: asString(value.detail_code) ?? null,
    child_command: asString(value.child_command) ?? null,
    exit_code: asExitCode(value.exit_code),
    message: asString(value.message) ?? null,
  };
}

function normalizePipelineExecution(value: unknown): PipelineExecution | null {
  if (!isRecord(value)) return null;
  return {
    ...value,
    name: asString(value.name) ?? null,
    status: asString(value.status),
    log_url: asString(value.log_url) ?? null,
    log_state: typeof value.log_state === 'string' ? value.log_state : null,
    log_state_reason: asString(value.log_state_reason) ?? null,
    diagnostic: normalizePipelineDiagnostic(value.diagnostic),
    diagnostic_state: asString(value.diagnostic_state) ?? null,
    failure_reason: asString(value.failure_reason) ?? null,
    quota_settlement: asString(value.quota_settlement) ?? null,
  };
}

function asBoolean(value: unknown): boolean | undefined {
  return typeof value === 'boolean' ? value : undefined;
}

function normalizePipelineQuota(value: unknown): PipelineQuota | null {
  if (!isRecord(value)) return null;
  return {
    enforced: asBoolean(value.enforced) ?? false,
    allowed: asBoolean(value.allowed) ?? false,
    reason: asString(value.reason),
    message: asString(value.message),
    runs_today: asNumber(value.runs_today) ?? 0,
    daily_limit: asNumber(value.daily_limit) ?? 0,
    cooldown_until: asString(value.cooldown_until) ?? null,
    next_reset: asString(value.next_reset) ?? null,
    new_raw_files: asNumber(value.new_raw_files) ?? 0,
    min_new_raw: asNumber(value.min_new_raw) ?? 0,
    already_running: asBoolean(value.already_running) ?? false,
  };
}

function firstString(record: Record<string, unknown>, keys: string[]) {
  for (const key of keys) {
    const value = asString(record[key]);
    if (value) return value;
  }
}

function firstNumber(record: Record<string, unknown>, keys: string[]) {
  for (const key of keys) {
    const value = asNumber(record[key]);
    if (value !== undefined) return value;
  }
}

function stringArray(value: unknown): string[] {
  return Array.isArray(value)
    ? value.filter((item): item is string => typeof item === 'string' && item.trim().length > 0)
    : [];
}

function extractArray(payload: unknown): unknown[] {
  if (Array.isArray(payload)) return payload;
  if (!isRecord(payload)) return [];

  for (const key of ['items', 'results', 'sources', 'concepts', 'data']) {
    const value = payload[key];
    if (Array.isArray(value)) return value;
  }

  return [];
}

function extractNamedArray(payload: unknown, keys: string[]): unknown[] {
  if (Array.isArray(payload)) return payload;
  if (!isRecord(payload)) return [];
  for (const key of keys) {
    const value = payload[key];
    if (Array.isArray(value)) return value;
  }
  return [];
}

function normalizeAdminProject(item: unknown): AdminProject | null {
  const record = isRecord(item) ? item : {};
  // Prefer Firestore compound doc id; fall back only when legacy payloads omit it.
  const projectId = firstString(record, ['project_id', 'projectId']) ?? '';
  const id = firstString(record, ['id']) ?? projectId;
  const name = firstString(record, ['name', 'project_name', 'projectName']) ?? id;
  const userId = firstString(record, ['user_id', 'userId', 'uid']) ?? '';
  const userName = firstString(record, ['user_name', 'userName']) ?? '';
  const userEmail = firstString(record, ['user_email', 'userEmail']) ?? '';
  if (!id || !name) return null;
  return {
    id,
    name,
    projectId,
    userId,
    userName,
    userEmail,
    conceptCount: firstNumber(record, ['concept_count', 'conceptCount', 'concepts_count', 'conceptsCount']) ?? 0,
    sourceCount: firstNumber(record, ['source_count', 'sourceCount', 'sources_count', 'sourcesCount']) ?? 0,
  };
}

function normalizeAdminUser(item: unknown): AdminUser | null {
  const record = isRecord(item) ? item : {};
  const id = firstString(record, ['id', 'user_id', 'userId']);
  const email = firstString(record, ['email']) ?? '';
  const name = firstString(record, ['name', 'user_name', 'userName']) ?? '';
  const scopeStates = isRecord(record.scope_states) ? record.scope_states : {};
  if (!id) return null;
  return {
    id,
    name,
    email,
    status: firstString(record, ['status']) ?? 'active',
    scopeStates: Object.fromEntries(
      (['auth:admin', 'lwc:admin'] as const).flatMap((scope) => {
        const value = scopeStates[scope];
        if (!isRecord(value)) return [];
        return [[scope, {
          state: firstString(value, ['state']) ?? 'unknown',
          revision: firstNumber(value, ['revision']) ?? 0,
          source: firstString(value, ['source']) ?? '',
        }]];
      }),
    ),
    projectCount: firstNumber(record, ['project_count', 'projectCount', 'projects_count', 'projectsCount']) ?? 0,
  };
}

export function normalizeEntry(item: unknown): WikiEntry {
  if (typeof item === 'string') {
    return { slug: item, title: item.replaceAll('-', ' '), raw: item };
  }

  const record = isRecord(item) ? item : {};
  const frontmatter = isRecord(record.frontmatter)
    ? record.frontmatter
    : isRecord(record.metadata)
      ? record.metadata
      : undefined;
  const title =
    firstString(record, ['title', 'name', 'slug', 'id']) ??
    firstString(frontmatter ?? {}, ['title', 'name']) ??
    'Untitled';
  const slug =
    firstString(record, ['slug', 'id', 'path']) ??
    title.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/(^-|-$)/g, '');

  return {
    slug,
    title,
    id: firstString(record, ['id']) ?? firstString(frontmatter ?? {}, ['id']),
    status: firstString(record, ['status']) ??
      firstString(frontmatter ?? {}, ['status']),
    description: firstString(record, ['description', 'summary', 'excerpt']) ??
      firstString(frontmatter ?? {}, ['description', 'summary']),
    date: firstString(record, ['date', 'createdAt', 'updatedAt']) ??
      firstString(frontmatter ?? {}, ['date', 'createdAt', 'updatedAt']),
    frontmatter,
    content: firstString(record, ['content', 'markdown', 'body', 'text']),
    annotationAllowed: asBoolean(record.annotation_allowed) ?? asBoolean(record.annotationAllowed),
    raw: item,
  };
}

function normalizeSourceLifecycle(value: unknown): SourceLifecycle {
  switch (value) {
    case 'new':
    case 'synced':
    case 'notes_pending':
    case 'content_pending':
    case 'error':
      return value;
    default:
      return 'synced';
  }
}

export function normalizeSourceListItem(item: unknown): SourceListItem {
  const entry = normalizeEntry(item);
  const record = isRecord(item) ? item : {};
  const rawPath = firstString(record, ['raw_path', 'rawPath', 'raw_file_path', 'rawFilePath', 'source_file', 'path']) ?? entry.slug;

  return {
    id: firstString(record, ['id', 'source_id', 'sourceId']),
    slug: entry.slug,
    title: firstString(record, ['title', 'name']) ?? rawPath,
    rawPath,
    lifecycle: normalizeSourceLifecycle(firstString(record, ['lifecycle_status', 'lifecycleStatus', 'lifecycle', 'lifecycle_state', 'lifecycleState', 'state', 'status'])),
    annotationPresent: asBoolean(record.annotation_present) ?? asBoolean(record.annotationPresent) ?? asBoolean(record.has_annotation) ?? false,
    annotationAllowed: asBoolean(record.annotation_allowed) ?? asBoolean(record.annotationAllowed) ?? false,
    error: firstString(record, ['error', 'error_message', 'errorMessage']),
    raw: item,
  };
}

function normalizeAnnotation(payload: unknown): SourceAnnotation {
  const record = isRecord(payload) ? payload : {};
  const generation = normalizeAnnotationGeneration(
    record.expected_generation ?? record.expectedGeneration ?? record.generation,
  );
  if (!generation) {
    throw new ApiError('Invalid annotation response: expected_generation is required', 500);
  }
  return {
    body: firstString(record, ['body', 'annotation']) ?? '',
    expectedGeneration: generation,
    hasAnnotation: asBoolean(record.has_annotation) ?? asBoolean(record.hasAnnotation) ?? false,
    annSha256: firstString(record, ['ann_sha256', 'annSha256']),
    annotationDirty: asBoolean(record.annotation_dirty) ?? asBoolean(record.annotationDirty) ?? false,
    rawDirty: asBoolean(record.raw_dirty) ?? asBoolean(record.rawDirty) ?? false,
    dirty: asBoolean(record.dirty) ?? false,
    lifecycleStatus: firstString(record, ['lifecycle_status', 'lifecycleStatus']),
    updatedAt: firstString(record, ['updated_at', 'updatedAt']),
    updatedBy: firstString(record, ['updated_by', 'updatedBy']),
  };
}

export { normalizeAnnotationBody };

export function normalizeSearchResult(item: unknown): SearchResult {
  const entry = normalizeEntry(item);
  const record = isRecord(item) ? item : {};

  return {
    ...entry,
    excerpt: firstString(record, ['excerpt', 'snippet', 'summary', 'description']),
    score: firstNumber(record, ['score', 'rank']),
    type: firstString(record, ['type', 'kind', 'collection']),
  };
}

export function normalizeCitation(item: unknown): Citation | null {
  const record = isRecord(item) ? item : {};
  const text = firstString(record, ['text', 'title', 'name']);
  const rawType = firstString(record, ['type', 'kind', 'collection']);
  const path = firstString(record, ['path', 'href', 'url']);
  const explicitSlug = safeWikiRouteSegment(firstString(record, ['slug']));
  const explicitId = safeWikiRouteSegment(firstString(record, ['id', 'concept_id', 'source_id', 'sourceId', 'conceptId']));

  const normalizedType = rawType?.replace(/s$/, '');
  if (normalizedType !== 'concept' && normalizedType !== 'source') return null;

  const pathSlug = citationPathSegment(normalizedType, path);
  const slug = explicitSlug || pathSlug;
  if (!text || (!explicitId && !slug)) return null;

  const safePath = pathSlug ? path : undefined;

  return {
    text,
    slug: slug ?? undefined,
    type: normalizedType,
    id: explicitId ?? undefined,
    path: safePath,
  };
}

function normalizeQueryExpansion(value: unknown): QueryExpansion | undefined {
  const record = isRecord(value) ? value : {};
  const keywords = stringArray(record.keywords);
  return keywords.length > 0 ? { keywords } : undefined;
}

function isQueryOperationFailure(value: unknown): value is QueryOperationFailure {
  if (!isRecord(value)) return false;
  return (value.stage === 'query_expansion' || value.stage === 'answer_synthesis')
    && (!('source' in value) || ['personal', 'shared', 'unknown'].includes(String(value.source)))
    && ['authentication', 'rate_limited', 'quota_exhausted', 'billing', 'timeout', 'invalid_response', 'output_truncated', 'provider_unavailable', 'provider_error', 'context_unavailable', 'canceled'].includes(String(value.class))
    && ['none', 'raw_query', 'deterministic_plan', 'retrieval_only'].includes(String(value.fallback));
}

function normalizeQueryOperationFailures(value: unknown): QueryOperationFailure[] {
  return Array.isArray(value) ? value.filter(isQueryOperationFailure) : [];
}

export function normalizeSearchResponse(payload: unknown): SearchResponse {
  const record = isRecord(payload) ? payload : {};
  const citationItems = Array.isArray(record.citations) ? record.citations : [];

  return {
    status: asString(record.status),
    reason: asString(record.reason),
    answer_basis: asString(record.answer_basis),
    wiki_evidence_status: asString(record.wiki_evidence_status),
    disclosure_required: asBoolean(record.disclosure_required),
    ai_operation_failures: normalizeQueryOperationFailures(record.ai_operation_failures),
    results: extractArray(payload).map(normalizeSearchResult),
    aiAnswer: firstString(record, ['ai_synth', 'ai_answer', 'aiAnswer', 'answer']) ?? '',
    citations: citationItems
      .map(normalizeCitation)
      .filter((citation): citation is Citation => citation !== null),
    expand: normalizeQueryExpansion(record.expand),
  };
}

export function normalizeStatus(payload: unknown): ApiStatus {
  const record = isRecord(payload) ? payload : {};
  const sourcesCount =
    firstNumber(record, ['sourcesCount', 'sourceCount', 'sources_count']) ??
    (Array.isArray(record.sources) ? record.sources.length : 0);
  const conceptsCount =
    firstNumber(record, ['conceptsCount', 'conceptCount', 'concepts_count']) ??
    (Array.isArray(record.concepts) ? record.concepts.length : 0);
  const rawCount =
    firstNumber(record, ['rawCount', 'raw_count', 'filesCount', 'files_count']) ?? 0;

  const lastExecution = normalizePipelineExecution(record.last_execution);
  const scheduledExecution = normalizePipelineExecution(record.scheduled_execution);
  const suggestedQueries = stringArray(record.suggested_queries ?? record.suggestedQueries);

  return {
    sourcesCount,
    conceptsCount,
    rawCount,
    suggestedQueries,
    lastExecution,
    scheduledExecution,
    quota: normalizePipelineQuota(record.quota),
	scheduledPipeline: normalizeScheduledPipelineStatus(record.scheduled_pipeline),
    raw: record,
  };
}

function normalizeScheduledPipelineStatus(value: unknown): ScheduledPipelineStatus | null {
  if (!isRecord(value)) return null;
  return {
    availability: asString(value.availability) ?? 'unavailable',
    schedule: asString(value.schedule) ?? '',
    timezone: asString(value.timezone) ?? '',
    last_check_at: asString(value.last_check_at) ?? null,
    next_check_at: asString(value.next_check_at) ?? null,
    skip_reason: asString(value.skip_reason) ?? '',
    pending_work: firstNumber(value, ['pending_work']) ?? 0,
    new_raw_files: firstNumber(value, ['new_raw_files']) ?? 0,
    raw_dirty_files: firstNumber(value, ['raw_dirty_files']) ?? 0,
    annotation_dirty_files: firstNumber(value, ['annotation_dirty_files']) ?? 0,
    day_key: asString(value.day_key) ?? '',
    budget_used: asBoolean(value.budget_used) ?? false,
    last_attempt_at: asString(value.last_attempt_at) ?? null,
    last_attempt_id: asString(value.last_attempt_id) ?? null,
    last_execution_id: asString(value.last_execution_id) ?? null,
    last_result: asString(value.last_result) ?? null,
  };
}

export function normalizeRawFile(item: unknown): RawFile {
  const record = isRecord(item) ? item : {};
  const name = firstString(record, ['name', 'filename', 'path']) ?? 'unnamed';

  return {
    name,
    size: firstNumber(record, ['size', 'bytes']) ?? 0,
    updated: firstString(record, ['updated', 'updated_at', 'updatedAt']) ?? '',
    sha256: firstString(record, ['sha256', 'digest']) ?? '',
    ingested: asBoolean(record.ingested) ?? false,
    raw: item,
  };
}

export async function getStatus(projectId?: string) {
  return normalizeStatus(await requestJson<unknown>('/api/v1/status', { projectId }));
}

export async function getRawFiles(projectId?: string) {
  return extractNamedArray(await requestJson<unknown>('/api/v1/raw', { projectId }), ['files', 'items', 'results', 'data'])
    .map(normalizeRawFile);
}

export async function getRawFilePreview(filename: string, projectId?: string): Promise<string> {
  if (!isValidRawRelativePath(filename)) {
    throw new ApiError('Invalid raw-relative path', 400);
  }
  const rawPath = filename.split('/').map(encodeURIComponent).join('/');
  const response = await apiFetch(`/api/v1/raw/${rawPath}?preview=true`, { projectId });

  if (!response.ok) {
    const payload: unknown = await response.json().catch(() => null);
    throw new ApiError(
      `Raw preview request failed (${response.status})`,
      response.status,
      diagnosticIdFromPayload(payload),
      response.headers.get('X-Request-ID'),
    );
  }

  return response.text();
}

export async function getSources(projectId?: string) {
  return extractArray(await requestJson<unknown>('/api/v1/sources', { projectId })).map(normalizeSourceListItem);
}

async function documentImportRequest<T>(path: string, options: ApiFetchOptions = {}): Promise<T> {
  const response = await apiFetch(path, options);
  const payload: unknown = response.status === 204 ? null : await response.json().catch(() => null);
  if (!response.ok) {
    const message = isRecord(payload) ? asString(payload.error) ?? asString(payload.message) : undefined;
    throw new ApiError(message ?? `Document import request failed (${response.status})`, response.status,
      diagnosticIdFromPayload(payload), response.headers.get('X-Request-ID'));
  }
  return payload as T;
}

export async function getDocumentImports(projectId: string, conversionStatus = 'all', cursor?: string): Promise<DocumentImportList> {
  const query = new URLSearchParams({ conversion_status: conversionStatus, limit: '100' });
  if (cursor) query.set('cursor', cursor);
  return documentImportRequest<DocumentImportList>(`/api/v1/projects/${encodeURIComponent(projectId)}/document-imports?${query}`, { projectId });
}

export async function getDocumentImport(projectId: string, documentId: string): Promise<DocumentImportRecord> {
  return documentImportRequest<DocumentImportRecord>(`/api/v1/projects/${encodeURIComponent(projectId)}/document-imports/${encodeURIComponent(documentId)}`, { projectId });
}

export async function getDocumentImportBySubmission(projectId: string, submissionId: string): Promise<Record<string, unknown>> {
  return documentImportRequest<Record<string, unknown>>(`/api/v1/projects/${encodeURIComponent(projectId)}/document-imports/by-submission/${encodeURIComponent(submissionId)}`, { projectId });
}

export async function getDocumentImportFailureSummary(projectId: string): Promise<DocumentImportSummary> {
  return documentImportRequest<DocumentImportSummary>(`/api/v1/projects/${encodeURIComponent(projectId)}/document-imports/failure-summary`, { projectId });
}

export async function dismissDocumentImportFailureReminder(projectId: string, sequence: number): Promise<DocumentImportSummary> {
  return documentImportRequest<DocumentImportSummary>(`/api/v1/projects/${encodeURIComponent(projectId)}/document-imports/failure-reminder`, {
    method: 'PUT', projectId, json: true, body: JSON.stringify({ dismissed_through_sequence: sequence }),
  });
}

export async function downloadDocumentImportOriginal(projectId: string, documentId: string): Promise<Blob> {
  const response = await apiFetch(`/api/v1/projects/${encodeURIComponent(projectId)}/document-imports/${encodeURIComponent(documentId)}/original`, { projectId });
  if (!response.ok) {
    const payload: unknown = await response.json().catch(() => null);
    throw new ApiError(`Original file download failed (${response.status})`, response.status, diagnosticIdFromPayload(payload), response.headers.get('X-Request-ID'));
  }
  return response.blob();
}

export async function deleteDocumentImport(projectId: string, documentId: string, revision: number): Promise<void> {
  await documentImportRequest<void>(`/api/v1/projects/${encodeURIComponent(projectId)}/document-imports/${encodeURIComponent(documentId)}`, {
    method: 'DELETE', projectId, headers: { 'If-Match': String(revision) },
  });
}

export async function getSource(slug: string) {
	const safeSlug = safeWikiRouteSegment(slug);
	if (!safeSlug) throw new ApiError('Invalid source lookup segment', 400);
  return normalizeEntry(
    await requestJson<unknown>(`/api/v1/sources/${encodeURIComponent(safeSlug)}`),
  );
}

export async function getSourceAnnotation(sourceId: string): Promise<SourceAnnotation> {
  const response = await apiFetch(`/api/v1/sources/${encodeURIComponent(sourceId)}/annotation`);
  if (!response.ok) {
    const payload: unknown = await response.json().catch(() => null);
    throw new ApiError(
      `Annotation request failed (${response.status})`,
      response.status,
      diagnosticIdFromPayload(payload),
      response.headers.get('X-Request-ID'),
    );
  }
  return normalizeAnnotation(await response.json());
}

export async function updateSourceAnnotation(
  sourceId: string,
  body: string,
  expectedGeneration: string,
): Promise<SourceAnnotation> {
  const response = await apiFetch(`/api/v1/sources/${encodeURIComponent(sourceId)}/annotation`, {
    method: 'PUT',
    json: true,
    body: JSON.stringify({ body: normalizeAnnotationBody(body), expected_generation: String(expectedGeneration) }),
  });
  if (!response.ok) {
    const payload: unknown = await response.json().catch(() => null);
    throw new ApiError(
      `Annotation update failed (${response.status})`,
      response.status,
      diagnosticIdFromPayload(payload),
      response.headers.get('X-Request-ID'),
    );
  }
  return normalizeAnnotation(await response.json());
}

export async function getConcepts() {
  return extractArray(await requestJson<unknown>('/api/v1/concepts')).map(normalizeEntry);
}

export async function getConcept(slug: string) {
	const safeSlug = safeWikiRouteSegment(slug);
	if (!safeSlug) throw new ApiError('Invalid concept lookup segment', 400);
  return normalizeEntry(
    await requestJson<unknown>(`/api/v1/concepts/${encodeURIComponent(safeSlug)}`),
  );
}

export async function searchWiki(query: string, mode: 'wiki' | 'full') {
  const payload = await postJson<unknown>('/api/query', { q: query, mode });
  const responseFields = ['results', 'items', 'sources', 'concepts', 'data', 'ai_synth', 'ai_answer', 'aiAnswer', 'answer', 'status'];
  if (!Array.isArray(payload) && (!isRecord(payload) || !responseFields.some((key) => key in payload))) {
    throw new QueryApiError('The query response did not match the expected schema.', 502, 'invalid_response', diagnosticIdFromPayload(payload));
  }
  if (isRecord(payload)) {
    const arrayFields = ['results', 'items', 'sources', 'concepts', 'data', 'citations'];
    const hasInvalidArray = arrayFields.some((key) => key in payload && !Array.isArray(payload[key]));
    const hasInvalidExpand = 'expand' in payload && payload.expand !== null && !isRecord(payload.expand);
    const hasInvalidAIOperationFailures = 'ai_operation_failures' in payload
      && (!Array.isArray(payload.ai_operation_failures) || payload.ai_operation_failures.some((item) => !isQueryOperationFailure(item)));
    const hasInvalidMetadata = ['status', 'reason', 'answer_basis', 'wiki_evidence_status']
      .some((key) => key in payload && payload[key] !== null && typeof payload[key] !== 'string')
      || ('disclosure_required' in payload && payload.disclosure_required !== null && typeof payload.disclosure_required !== 'boolean');
    if (hasInvalidArray || hasInvalidExpand || hasInvalidMetadata || hasInvalidAIOperationFailures) {
      throw new QueryApiError('The query response did not match the expected schema.', 502, 'invalid_response', diagnosticIdFromPayload(payload));
    }
  }
  return normalizeSearchResponse(payload);
}

// ── Raw content management ──

export type RawUploadStatus = 'created' | 'already_exists' | 'replaced';

export type RawUploadResult = {
  filename: string;
  path: string;
  bytes: number;
  sha256: string;
  status: RawUploadStatus;
};

export type DocumentImportRecord = {
  document_id: string;
  original_filename: string;
  media_type: string;
  bytes: number;
  sha256: string;
  created_at: string;
  updated_at: string;
  revision: number;
  converter_version: string;
  dependency_lock_digest?: string;
  conversion_state: 'receiving' | 'accepted' | 'queued' | 'converting' | 'succeeded' | 'failed' | string;
  source_state: 'not_started' | 'saving' | 'saved' | 'failed' | 'unknown' | string;
  failure?: { stage: string; class: string; safe_message_key: string; diagnostic_id?: string; retryable: boolean } | null;
  source_ref?: { raw_path: string; sha256: string; object_generation: number; source_id?: string } | null;
  pipeline_ref?: { status: string; intent_id?: string; execution_id?: string; content_generation?: string; reason?: string } | null;
  failure_sequence?: number;
  deletion_state: 'present' | 'deleting' | 'delete_failed' | 'deleted' | string;
};

export type DocumentImportSummary = {
  failed_count: number;
  latest_failure_sequence: number;
  reminder_visible: boolean;
  dismissed_through_sequence: number;
  site_url: string;
  snapshot_revision: number;
};

export type DocumentImportList = {
  items: DocumentImportRecord[];
  next_cursor?: string;
  snapshot_revision: number;
  failed_count: number;
  latest_failure_sequence: number;
};

export type DocumentImportUploadResult = DocumentImportRecord & { status_url: string; site_url: string; status?: RawUploadStatus };
export type RawFileUploadResult = RawUploadResult | DocumentImportUploadResult;

export type RawUploadProgressCallback = (progress: number) => void;

export const RAW_UPLOAD_MAX_BYTES = 5 << 20;

export type ScrapeStatus = 'preview' | 'stored' | 'needs_confirmation' | 'rejected' | 'conflict' | 'unavailable' | 'uncertain';

export type ScrapeCandidatePreview = {
  title: string;
  source_url: string;
  markdown: string;
  metadata: Record<string, unknown>;
  candidate_sha256: string;
  quality_checked?: boolean;
  quality_status?: 'usable' | 'needs_confirmation' | 'rejected' | 'not_checked';
  reason?: string;
  warning?: string;
  provider: string;
  provider_truncated?: boolean;
};

export type ScrapeFailureDiagnostic = {
  dependency: 'deepseek-fetch' | 'unknown';
  operation: 'fetch';
  category:
    | 'not_configured'
    | 'invalid_url'
    | 'authentication'
    | 'balance'
    | 'security_policy'
    | 'content_unavailable'
    | 'rate_limited'
    | 'provider_timeout'
    | 'provider_error'
    | 'invalid_request'
    | 'request_too_large'
    | 'unexpected_status'
    | 'transport_timeout'
    | 'transport_cancelled'
    | 'transport_error'
    | 'invalid_response'
    | 'content_too_large'
    | 'unknown';
  upstream_status?: number;
};

export type ScrapeResult = {
  status: ScrapeStatus;
  message: string;
  confirmation_ref?: string;
  candidate_id?: string;
  candidate_revision?: string;
  expires_at?: string;
  retry_after_seconds?: number;
  failure_stage?: 'setup' | 'validation' | 'fetch' | 'size' | 'quality' | 'storage' | 'candidate' | 'commit';
  failure_diagnostic?: ScrapeFailureDiagnostic;
  preview?: ScrapeCandidatePreview;
  storage?: { path: string; sha256: string; status: RawUploadStatus };
  processing_status?: 'not_started' | string;
};

export type PipelineQuota = {
  enforced: boolean;
  allowed: boolean;
  reason?: string;
  message?: string;
  runs_today: number;
  daily_limit: number;
  cooldown_until?: string | null;
  next_reset?: string | null;
  new_raw_files: number;
  min_new_raw: number;
  already_running: boolean;
};

export type ScheduledPipelineStatus = {
  availability: string;
  schedule: string;
  timezone: string;
  last_check_at: string | null;
  next_check_at: string | null;
  skip_reason: string;
  pending_work: number;
  new_raw_files: number;
  raw_dirty_files: number;
  annotation_dirty_files: number;
  day_key: string;
  budget_used: boolean;
  last_attempt_at: string | null;
  last_attempt_id: string | null;
  last_execution_id: string | null;
  last_result: string | null;
};

export type PipelineResult = {
  status?: 'accepted' | string;
  execution_id?: string;
  intent_id?: string;
  intent_status?: string;
  project_id?: string;
  compile_mode?: 'incremental' | 'full' | string;
  message?: string;
  command?: string;
  quota?: PipelineQuota | null;
  rawFiles?: number;
  scheduled?: boolean;
};

export type AdminPipelineResult = PipelineResult & { stage?: string };

export type PipelinePreparedGuidance = {
  kind: 'active' | 'bootstrap' | string;
  status?: string;
  revision?: string;
  source_profile_revision?: number;
  source_input_digest?: string;
  model_version?: string;
  prompt_version?: string;
  schema_version?: string;
  candidate_id?: string;
  content_generation?: string;
};

export type PipelineIntentStatus = {
  id: string;
  status: string;
  compile_mode: 'incremental' | 'full' | string;
  execution_id?: string;
  saved_profile_revision?: number;
  saved_profile_input_digest?: string;
  prepared_guidance?: PipelinePreparedGuidance | null;
  error_stage?: string;
  error_code?: string;
  error_message?: string;
  created_at?: string;
  updated_at?: string;
};

export type PipelineStatus = {
  last_execution?: PipelineExecution | null;
  pipeline_intent?: PipelineIntentStatus | null;
  project_id?: string;
  quota?: PipelineQuota | null;
  full_quota?: PipelineQuota | null;
  scheduled_pipeline?: ScheduledPipelineStatus | null;
  scheduled_execution?: PipelineExecution | null;
  suggested_queries?: string[];
};

type XhrResponse = {
  status: number;
  ok: boolean;
  requestId: string | null;
  text: () => Promise<string>;
  json: () => Promise<unknown>;
};

function xhrResponse(xhr: XMLHttpRequest): XhrResponse {
  const body = xhr.responseText ?? '';
  const requestId = typeof xhr.getResponseHeader === 'function'
    ? xhr.getResponseHeader('X-Request-ID')
    : null;
  recordRequestIDHeader(requestId, xhr.status);
  return {
    status: xhr.status,
    ok: xhr.status >= 200 && xhr.status < 300,
    requestId,
    text: async () => body,
    json: async () => JSON.parse(body),
  };
}

function uploadErrorMessage(body: unknown): string {
  if (!isRecord(body)) return 'Upload failed';
  return asString(body.error) ?? asString(body.detail) ?? asString(body.message) ?? 'Upload failed';
}

function uploadApiError(body: unknown, status: number, requestId?: string | null): ApiError {
  return new ApiError(uploadErrorMessage(body), status, diagnosticIdFromPayload(body), requestId);
}

function sendRawUploadRequest(
  formData: FormData,
  projectId: string,
  accessToken: string,
  reportProgress: (progress: number) => void,
  submissionID?: string,
  batchID?: string,
): Promise<XhrResponse> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open('POST', `${getRuntimeConfig().api_url}${toV1Path('/api/v1/raw/upload')}`);
    xhr.withCredentials = true;

    for (const [name, value] of Object.entries(buildProjectHeaders(projectId, accessToken))) {
      xhr.setRequestHeader(name, value);
    }
    if (submissionID) formData.set('client_submission_id', submissionID);
    if (batchID) formData.set('batch_id', batchID);

    xhr.upload.onprogress = (event) => {
      if (!event.lengthComputable || event.total <= 0) return;
      reportProgress(Math.round((event.loaded / event.total) * 100));
    };
    xhr.onload = () => resolve(xhrResponse(xhr));
    xhr.onerror = () => reject(new ApiError('Upload failed', 0));
    xhr.ontimeout = () => reject(new ApiError('Upload failed', 0));
    xhr.onabort = () => reject(new ApiError('Upload failed', 0));
    xhr.send(formData);
  });
}

export async function uploadRawFile(
  file: File,
  onProgress?: RawUploadProgressCallback,
  explicitProjectId?: string,
  submissionID?: string,
  batchID?: string,
): Promise<RawFileUploadResult> {
  if (file.size > RAW_UPLOAD_MAX_BYTES) {
    throw new Error('file too large (max 5 MiB)');
  }
  if (file.size === 0) {
    throw new Error('empty file');
  }

  const projectId = explicitProjectId ?? selectedProjectId();
  const { auth, accessToken, epoch: requestEpoch } = await accessTokenOrRefresh();
  const formData = new FormData();
  formData.append('file', file);
  const extension = file.name.toLowerCase().slice(file.name.lastIndexOf('.'));
  const isDocumentImport = ['.pdf', '.docx', '.pptx', '.xlsx', '.xls'].includes(extension);
  const documentSubmissionID = isDocumentImport ? submissionID ?? crypto.randomUUID() : undefined;
  if (documentSubmissionID) formData.set('client_submission_id', documentSubmissionID);
  let lastProgress = -1;
  const reportProgress = (progress: number) => {
    const bounded = Math.max(0, Math.min(100, Math.round(progress)));
    if (bounded === lastProgress) return;
    lastProgress = bounded;
    onProgress?.(bounded);
  };

  let response = await sendRawUploadRequest(
    formData,
    projectId,
    accessToken,
    reportProgress,
    documentSubmissionID,
    batchID,
  );
  if (
    response.status === 401
    && apiAuthConfig === auth
    && (auth.getSessionEpoch?.() ?? 0) === requestEpoch
  ) {
    const refreshed = await auth.refreshAccessToken();
    const refreshedOperation = asRefreshResult(refreshed);
    if (apiAuthConfig !== auth) return Promise.reject(
      uploadApiError(await response.json().catch(() => undefined), response.status, response.requestId),
    );
    if (refreshed === null) {
      if ((auth.getSessionEpoch?.() ?? 0) !== requestEpoch) {
        return Promise.reject(
          uploadApiError(await response.json().catch(() => undefined), response.status, response.requestId),
        );
      }
      auth.onUnauthorized(accessToken, requestEpoch);
    } else if (refreshedOperation === null) {
      return Promise.reject(
        uploadApiError(await response.json().catch(() => undefined), response.status, response.requestId),
      );
    } else if ((auth.getSessionEpoch?.() ?? 0) !== refreshedOperation.epoch) {
      return Promise.reject(
        uploadApiError(await response.json().catch(() => undefined), response.status, response.requestId),
      );
    }
    if (refreshedOperation) {
      response = await sendRawUploadRequest(
        formData,
        projectId,
        refreshedOperation.accessToken,
        reportProgress,
        documentSubmissionID,
        batchID,
      );
      if (
        response.status === 401
        && apiAuthConfig === auth
        && (auth.getSessionEpoch?.() ?? 0) === refreshedOperation.epoch
      ) {
        auth.onUnauthorized(refreshedOperation.accessToken, refreshedOperation.epoch);
      }
    }
  }

  if (!response.ok) {
    const body = await response.json().catch(() => undefined);
    throw uploadApiError(body, response.status, response.requestId);
  }

  reportProgress(100);
  const body = (await response.json()) as Partial<RawUploadResult> & Record<string, unknown>;
  if (isDocumentImport) {
    if (response.status !== 202 || typeof body.document_id !== 'string' || typeof body.status_url !== 'string') {
      throw new ApiError('The document acceptance response was incomplete.', 502, undefined, response.requestId);
    }
    return body as unknown as DocumentImportUploadResult;
  }
  const status = body.status === 'already_exists' || body.status === 'replaced'
    ? body.status
    : response.status === 200 ? 'already_exists' : 'created';
  return {
    filename: typeof body.filename === 'string' ? body.filename : file.name,
    path: typeof body.path === 'string' ? body.path : '',
    bytes: typeof body.bytes === 'number' ? body.bytes : file.size,
    sha256: typeof body.sha256 === 'string' ? body.sha256 : '',
    status,
  };
}

async function postScrapeRequest(body: Record<string, unknown>, projectId?: string): Promise<ScrapeResult> {
  const requestState: { dispatched: boolean; pending: boolean; response: Response | null } = {
    dispatched: false,
    pending: false,
    response: null,
  };
  let response: Response;
  try {
    response = await apiFetch('/api/v1/raw/scrape', {
      method: 'POST',
      json: true,
      projectId,
      body: JSON.stringify(body),
      onRequestDispatched: () => {
        requestState.dispatched = true;
        requestState.pending = true;
        requestState.response = null;
      },
      onResponseReceived: (received) => {
        requestState.pending = false;
        requestState.response = received;
      },
    });
  } catch (error) {
    const { dispatched, pending, response: lastResponse } = requestState;
    if (!dispatched) throw error;
    if (!pending && lastResponse && lastResponse.status >= 400 && lastResponse.status < 500) {
      throw new ApiError('URL intake was rejected before storage.', lastResponse.status, undefined, lastResponse.headers.get('X-Request-ID'));
    }
    throw new ApiError('URL intake response was lost; check raw/ before retrying.', 0);
  }
  const payload: unknown = await response.json().catch(() => null);
  const structuredResult = payload && typeof payload === 'object' && 'status' in payload && 'message' in payload;
  if (structuredResult && (response.ok || (response.status >= 400 && response.status < 500))) {
    return payload as ScrapeResult;
  }
  if (!response.ok) {
    const message = payload && typeof payload === 'object' && 'error' in payload && typeof payload.error === 'string'
      ? payload.error
      : 'URL intake failed';
    if (response.status >= 400 && response.status < 500) {
      throw new ApiError(message, response.status, undefined, response.headers.get('X-Request-ID'));
    }
    throw new ApiError('URL intake response was lost; check raw/ before retrying.', 0, undefined, response.headers.get('X-Request-ID'));
  }
  throw new ApiError('URL intake returned an invalid response; check raw/ before retrying.', 0, undefined, response.headers.get('X-Request-ID'));
}

export async function scrapeUrl(url: string, projectId?: string, tags?: string[]): Promise<ScrapeResult> {
  return postCandidateRequest('/api/v1/raw/scrape', { url, ...(tags?.length ? { tags } : {}) }, projectId, 'prepare');
}

export async function acceptScrapeCandidate(candidateId: string, candidateRevision: string, title: string, projectId?: string): Promise<ScrapeResult> {
  return postCandidateRequest('/api/v1/raw/scrape/accept', { candidate_id: candidateId, candidate_revision: candidateRevision, title }, projectId, 'commit', {
    candidate_id: candidateId, candidate_revision: candidateRevision,
  });
}

export async function rejectScrapeCandidate(candidateId: string, candidateRevision: string, projectId?: string): Promise<ScrapeResult> {
  return postCandidateRequest('/api/v1/raw/scrape/reject', { candidate_id: candidateId, candidate_revision: candidateRevision }, projectId, 'reject');
}

export async function resolveScrapeCandidate(candidateId: string, projectId?: string): Promise<ScrapeResult> {
  return postCandidateRequest('/api/v1/raw/scrape/resolve', { candidate_id: candidateId }, projectId, 'resolve', { candidate_id: candidateId });
}

async function postCandidateRequest(
  endpoint: string,
  body: Record<string, unknown>,
  projectId: string | undefined,
  action: 'prepare' | 'commit' | 'reject' | 'resolve',
  identity: Pick<ScrapeResult, 'candidate_id' | 'candidate_revision'> = {},
): Promise<ScrapeResult> {
  const state: { dispatched: boolean; pending: boolean; response: Response | null } = { dispatched: false, pending: false, response: null };
  let response: Response;
  try {
    response = await apiFetch(endpoint, {
      method: 'POST', json: true, projectId, body: JSON.stringify(body),
      onRequestDispatched: () => { state.dispatched = true; state.pending = true; state.response = null; },
      onResponseReceived: (received) => { state.pending = false; state.response = received; },
    });
  } catch (error) {
    const known = state.response;
    if (known && known.status >= 400 && known.status < 500) throw error;
    if (action === 'prepare') {
      if (!state.dispatched) throw error;
      throw new ApiError('URL preview response was lost; no raw article was saved.', 0);
    }
    if (!state.dispatched) throw error;
    if (action === 'commit' || action === 'resolve') {
      return { status: 'uncertain', message: 'Save status is uncertain. Check save status before retrying.', ...identity };
    }
    throw error;
  }
  const payload: unknown = await response.json().catch(() => null);
  if (payload && typeof payload === 'object' && 'status' in payload && 'message' in payload) {
    return { ...identity, ...(payload as ScrapeResult) };
  }
  if (!response.ok && response.status >= 400 && response.status < 500) {
    const message = payload && typeof payload === 'object' && 'error' in payload && typeof payload.error === 'string' ? payload.error : 'URL intake request was rejected';
    throw new ApiError(message, response.status, undefined, response.headers.get('X-Request-ID'));
  }
  if (action === 'commit' || action === 'resolve') {
    return { status: 'uncertain', message: 'Save status is uncertain. Check save status before retrying.', ...identity };
  }
  if (action === 'prepare') {
    return { status: 'unavailable', message: 'The preview could not be prepared. No article was saved; try again or use manual capture.' };
  }
  return { status: 'uncertain', message: 'Decline status is uncertain. Check candidate status.', ...identity };
}

export async function confirmScrape(confirmationRef: string, projectId?: string): Promise<ScrapeResult> {
  return postScrapeRequest({ confirmation_ref: confirmationRef }, projectId);
}

export async function triggerPipeline(): Promise<PipelineResult> {
  const response = await apiFetch('/api/v1/pipeline/run', {
    method: 'POST',
    json: true,
    requireProject: true,
  });
  if (!response.ok) {
    const error = await response.json().catch(() => ({ error: 'Pipeline trigger failed' }));
    throw new Error((error as { error: string }).error);
  }
  return response.json();
}

export async function retryPipeline(intentId: string, projectId: string): Promise<PipelineResult> {
  const response = await apiFetch('/api/v1/pipeline/retry', {
    method: 'POST',
    json: true,
    body: JSON.stringify({ intent_id: intentId }),
    projectId,
  });
  const payload: unknown = await response.json().catch(() => null);
  if (!response.ok) {
    throw new ApiError(profileApiError(payload, `Pipeline retry failed (${response.status})`), response.status, undefined, response.headers.get('X-Request-ID'));
  }
  if (!isRecord(payload) || payload.status !== 'accepted' || !asString(payload.intent_id)) {
    throw new ApiError('Invalid Pipeline retry response', 502);
  }
  return {
    status: 'accepted',
    intent_id: asString(payload.intent_id),
    intent_status: asString(payload.intent_status),
    execution_id: asString(payload.execution_id),
    project_id: asString(payload.project_id),
    compile_mode: asString(payload.compile_mode),
    ...(payload.quota == null ? {} : { quota: normalizePipelineQuota(payload.quota) }),
  };
}

export function getPipelineStatus(): Promise<PipelineStatus>;
export function getPipelineStatus(projectId: string, executionId?: string): Promise<PipelineStatus>;
export function getPipelineStatus(projectId: string, executionId?: string, intentId?: string): Promise<PipelineStatus>;
export async function getPipelineStatus(projectId?: string, executionId?: string, intentId?: string): Promise<PipelineStatus> {
  const query = new URLSearchParams();
  if (executionId) query.set('execution_id', executionId);
  if (intentId) query.set('intent_id', intentId);
  const suffix = query.size > 0 ? `?${query.toString()}` : '';
  return requestJson<PipelineStatus>(`/api/v1/pipeline/status${suffix}`, { projectId });
}

export async function getPipelineLog(logUrl: string, projectId?: string): Promise<string> {
  const response = await apiFetch(logUrl, { projectId });
  if (!response.ok) {
    throw new Error(`Pipeline log request failed (${response.status})`);
  }
  return response.text();
}

export async function generateTitle(content: string): Promise<string> {
  const response = await apiFetch('/api/v1/raw/generate-title', {
    method: 'POST',
    json: true,
    body: JSON.stringify({ content }),
  });
  if (!response.ok) return 'Untitled';
  const data = await response.json() as { title: string };
  return data.title ?? 'Untitled';
}

async function adminJson(path: string, options: Omit<ApiFetchOptions, 'requireProject'> = {}): Promise<unknown> {
  const response = await apiFetch(path, { ...options, requireProject: false });
  if (!response.ok) {
    const error = await response.json().catch(() => ({ error: `API request failed (${response.status})` }));
    throw new ApiError(
      (error as { error?: string }).error ?? `API request failed (${response.status})`,
      response.status,
      undefined,
      response.headers.get('X-Request-ID'),
    );
  }
  return response.json().catch(() => ({}));
}

export async function getAdminProjects(): Promise<AdminProject[]> {
  const payload = await adminJson('/api/v1/admin/projects');
  return extractNamedArray(payload, ['projects', 'items', 'results', 'data'])
    .map(normalizeAdminProject)
    .filter((project): project is AdminProject => project !== null);
}

export async function renameAdminProject(id: string, name: string): Promise<void> {
  await adminJson(`/api/v1/admin/projects/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    json: true,
    body: JSON.stringify({ name }),
  });
}

export async function deleteAdminProject(id: string): Promise<void> {
  await adminJson(`/api/v1/admin/projects/${encodeURIComponent(id)}`, { method: 'DELETE' });
}

export async function rebuildAdminProjectIndex(id: string): Promise<void> {
  await adminJson(`/api/v1/admin/projects/${encodeURIComponent(id)}/rebuild-index`, { method: 'POST' });
}

export async function triggerAdminProjectPipeline(
  id: string,
  options: { cleanRebuild?: boolean; stage?: 'full' | 'suggested-queries' } = {},
): Promise<AdminPipelineResult> {
  const cleanRebuild = options.cleanRebuild === true;
  const stage = options.stage === 'suggested-queries' ? 'suggested-queries' : 'full';
  const body: Record<string, unknown> = { stage };
  if (stage === 'full') {
    body.clean_rebuild = cleanRebuild;
  }
  return adminJson(`/api/v1/admin/projects/${encodeURIComponent(id)}/pipeline`, {
    method: 'POST',
    json: true,
    body: JSON.stringify(body),
  }) as Promise<AdminPipelineResult>;
}

export async function getAdminPipelineStatus(id: string, executionId: string, signal?: AbortSignal): Promise<PipelineStatus> {
  return adminJson(
    `/api/v1/admin/projects/${encodeURIComponent(id)}/pipeline/status?execution_id=${encodeURIComponent(executionId)}`,
    signal === undefined ? {} : { signal },
  ) as Promise<PipelineStatus>;
}

export async function getAdminUsers(): Promise<AdminUser[]> {
  const payload = await adminJson('/api/v1/admin/users');
  return extractNamedArray(payload, ['users', 'items', 'results', 'data'])
    .map(normalizeAdminUser)
    .filter((user): user is AdminUser => user !== null);
}

export async function getAdminManagementCatalog(): Promise<AdminManagementScope[]> {
  const payload = await adminJson('/api/v1/admin/management/catalog');
  const value = payload && typeof payload === 'object' ? payload as Record<string, unknown> : {};
  const scopes = Array.isArray(value.scopes) ? value.scopes : [];
  return scopes.filter((scope: unknown): scope is AdminManagementScope => {
    if (!scope || typeof scope !== 'object') return false;
    const value = scope as Partial<AdminManagementScope>;
    return (value.id === 'auth:admin' || value.id === 'lwc:admin') && value.grant_mode === 'operator-only' &&
      !!value.display_name && typeof value.display_name.en === 'string' && typeof value.display_name['zh-TW'] === 'string' &&
      !!value.description && typeof value.description.en === 'string' && typeof value.description['zh-TW'] === 'string';
  });
}

export async function updateAdminUserManagementScope(id: string, scope: 'auth:admin' | 'lwc:admin', action: 'grant' | 'remove'): Promise<void> {
  await adminJson(`/api/v1/admin/users/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    json: true,
    body: JSON.stringify({ scope, action, operation_id: crypto.randomUUID() }),
  });
}

export async function updateAdminUserStatus(id: string, status: 'active' | 'suspended'): Promise<void> {
  await adminJson(`/api/v1/admin/users/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    json: true,
    body: JSON.stringify({ status }),
  });
}

export async function deleteAdminUser(id: string): Promise<void> {
  await adminJson(`/api/v1/admin/users/${encodeURIComponent(id)}`, { method: 'DELETE' });
}

export type PublicConfig = {
  registration_enabled: boolean;
  email_registration_enabled: boolean;
  google_registration_enabled: boolean;
  demo_enabled: boolean;
  announcement_markdown?: string | null;
  announcement_digest?: string | null;
};

export type AdminSettings = {
  registration_enabled: boolean;
  email_registration_enabled: boolean;
  google_registration_enabled: boolean;
  announcement_markdown?: string | null;
};

function registrationSettings(record: Record<string, unknown>) {
  const preference = (key: string) => !Object.hasOwn(record, key) || record[key] === true;
  return {
    registration_enabled: record.registration_enabled === true,
    email_registration_enabled: preference('email_registration_enabled'),
    google_registration_enabled: preference('google_registration_enabled'),
  };
}

function registrationCapabilities(record: Record<string, unknown>) {
  const settings = registrationSettings(record);
  return {
    ...settings,
    email_registration_enabled: settings.registration_enabled && settings.email_registration_enabled,
    google_registration_enabled: settings.registration_enabled && settings.google_registration_enabled,
    demo_enabled: record.demo_enabled === true,
  };
}

let publicConfigCache: PublicConfig | null = null;

export function clearPublicConfigCache(): void {
  publicConfigCache = null;
}

/**
 * Public config — no auth.
 * Fail-closed: network/invalid/error → registration_enabled false (safer for 1.0).
 */
export async function getPublicConfig(options?: { refresh?: boolean }): Promise<PublicConfig> {
  if (!options?.refresh && publicConfigCache) return publicConfigCache;

  try {
    const response = await fetch(`${getRuntimeConfig().api_url}/api/v1/public/config`, {
      method: 'GET',
      credentials: 'omit',
    });
    recordRequestResponse(response);
    if (!response.ok) {
      const closed = registrationCapabilities({});
      publicConfigCache = closed;
      return closed;
    }
    const payload = (await response.json().catch(() => null)) as unknown;
    const record = isRecord(payload) ? payload : {};
    const config = {
      ...registrationCapabilities(record),
      ...(Object.hasOwn(record, 'announcement_markdown')
        ? { announcement_markdown: typeof record.announcement_markdown === 'string' ? record.announcement_markdown : null }
        : {}),
      ...(Object.hasOwn(record, 'announcement_digest')
        ? { announcement_digest: typeof record.announcement_digest === 'string' && /^sha256:[0-9a-f]{64}$/.test(record.announcement_digest) ? record.announcement_digest : null }
        : {}),
    } as PublicConfig;
    publicConfigCache = config;
    return config;
  } catch {
    const closed = registrationCapabilities({});
    publicConfigCache = closed;
    return closed;
  }
}

export async function getBuildInfo(): Promise<BuildInfo> {
  let response: Response;

  try {
    response = await fetch(`${getRuntimeConfig().api_url}/api/v1/public/version`, {
      method: 'GET',
      credentials: 'omit',
      cache: 'no-store',
    });
    recordRequestResponse(response);
  } catch (error) {
    const message = error instanceof Error ? error.message : 'unknown network error';
    throw new Error(`Build info request failed: ${message}`);
  }

  if (!response.ok) {
    throw new ApiError(`Build info request failed (${response.status})`, response.status, undefined, response.headers.get('X-Request-ID'));
  }

  let payload: unknown;
  try {
    payload = await response.json();
  } catch {
    throw new Error('Invalid build info response JSON');
  }

  if (!isRecord(payload)) {
    throw new Error('Invalid build info response: expected an object');
  }

  const field = (key: keyof BuildInfo): string => {
    const value = payload[key];
    if (typeof value !== 'string') {
      throw new Error(`Invalid build info response: ${key} must be a string`);
    }
    return value;
  };

  return {
    product_version: field('product_version'),
    commit: field('commit'),
    branch: field('branch'),
    tag: field('tag'),
    image_tag: field('image_tag'),
    service: field('service'),
    revision: field('revision'),
  };
}

export async function getAdminSettings(): Promise<AdminSettings> {
  const payload = await adminJson('/api/v1/admin/settings');
  const record = isRecord(payload) ? payload : {};
  if (typeof record.registration_enabled !== 'boolean') {
    throw new ApiError('Invalid admin settings response', 500);
  }
  return {
    ...registrationSettings(record),
    ...(Object.hasOwn(record, 'announcement_markdown') ? { announcement_markdown: typeof record.announcement_markdown === 'string' ? record.announcement_markdown : null } : {}),
  } as AdminSettings;
}

export async function getAdminAnnouncement(): Promise<string> {
  const payload = await adminJson('/api/v1/admin/settings/announcement');
  const record = isRecord(payload) ? payload : {};
  if (typeof record.announcement_markdown !== 'string') {
    throw new ApiError('Invalid admin announcement response', 500);
  }
  return record.announcement_markdown;
}

export async function updateAdminSettings(
  settings: Partial<AdminSettings>,
): Promise<AdminSettings> {
  const payload = await adminJson('/api/v1/admin/settings', {
    method: 'PATCH',
    json: true,
    body: JSON.stringify(settings),
  });
  const record = isRecord(payload) ? payload : {};
  if (typeof record.registration_enabled !== 'boolean') {
    throw new ApiError('Invalid admin settings response', 500);
  }
  clearPublicConfigCache();
  return {
    ...registrationSettings(record),
  } as AdminSettings;
}

export async function publishAnnouncement(announcement_markdown: string): Promise<{ announcement_markdown: string }> {
  const payload = await adminJson('/api/v1/admin/settings/announcement/publish', {
    method: 'POST',
    json: true,
    body: JSON.stringify({ announcement_markdown }),
  });
  const record = isRecord(payload) ? payload : {};
  if (typeof record.announcement_markdown !== 'string') {
    throw new ApiError('Invalid announcement publish response', 500);
  }
  clearPublicConfigCache();
  return { announcement_markdown: record.announcement_markdown };
}
