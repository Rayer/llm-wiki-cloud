import type { PipelineExecution } from './api';

export type PipelineLogAvailabilityState = 'pending' | 'available' | 'unavailable' | 'missing';

export type PipelineLogAvailability = {
  state: PipelineLogAvailabilityState;
  message: string | null;
  canOpen: boolean;
};

const LOG_REASON_MESSAGES: Record<string, string> = {
  unsupported_execution_status: 'Pipeline log is unavailable for this execution status.',
  storage_unavailable: 'Pipeline log storage is unavailable.',
  log_unavailable: 'Pipeline log is unavailable.',
  log_too_large: 'Pipeline log is too large to display.',
};

const GENERIC_UNAVAILABLE_MESSAGE = 'Pipeline log is unavailable.';

function unavailableMessage(reason: unknown): string {
  return typeof reason === 'string'
    ? LOG_REASON_MESSAGES[reason] ?? GENERIC_UNAVAILABLE_MESSAGE
    : GENERIC_UNAVAILABLE_MESSAGE;
}

function hasLogUrl(execution?: PipelineExecution | null): boolean {
  return typeof execution?.log_url === 'string' && execution.log_url.trim().length > 0;
}

export function getPipelineLogAvailability(
  execution?: PipelineExecution | null,
): PipelineLogAvailability {
  const logUrl = hasLogUrl(execution);
  const state = execution?.log_state;

  if (state == null) {
    return logUrl
      ? { state: 'available', message: null, canOpen: true }
      : { state: 'missing', message: 'No pipeline log is available.', canOpen: false };
  }

  switch (state) {
    case 'pending':
      return { state, message: 'Pipeline log is still pending.', canOpen: false };
    case 'available':
      return logUrl
        ? { state, message: null, canOpen: true }
        : { state: 'unavailable', message: GENERIC_UNAVAILABLE_MESSAGE, canOpen: false };
    case 'missing':
      return { state, message: 'No pipeline log is available.', canOpen: false };
    case 'unavailable':
      return { state, message: unavailableMessage(execution?.log_state_reason), canOpen: false };
    default:
      return { state: 'unavailable', message: GENERIC_UNAVAILABLE_MESSAGE, canOpen: false };
  }
}

export type PipelineLogRequestIdentity = {
  projectId: string;
  executionId: string;
  logUrl: string;
  nonce: number;
};

export type PipelineLogPanelState = {
  phase: 'never-opened' | 'loading' | 'loaded-empty' | 'loaded-nonempty' | 'error';
  projectId: string | null;
  identity: PipelineLogRequestIdentity | null;
  text: string;
  error: string;
};

export function initialPipelineLogState(projectId: string | null): PipelineLogPanelState {
  return {
    phase: 'never-opened',
    projectId,
    identity: null,
    text: '',
    error: '',
  };
}

function sameIdentity(
  left: PipelineLogRequestIdentity | null,
  right: PipelineLogRequestIdentity,
): boolean {
  if (!left) return false;
  return left.projectId === right.projectId &&
    left.executionId === right.executionId &&
    left.logUrl === right.logUrl &&
    left.nonce === right.nonce;
}

export function beginPipelineLogRequest(
  state: PipelineLogPanelState,
  identity: PipelineLogRequestIdentity,
): PipelineLogPanelState {
  if (state.phase === 'loading' || state.phase === 'loaded-empty' || state.phase === 'loaded-nonempty') {
    return state;
  }
  return {
    phase: 'loading',
    projectId: identity.projectId,
    identity,
    text: '',
    error: '',
  };
}

export function completePipelineLogRequest(
  state: PipelineLogPanelState,
  identity: PipelineLogRequestIdentity,
  text: string,
): PipelineLogPanelState {
  if (state.phase !== 'loading' || !sameIdentity(state.identity, identity)) return state;
  return {
    ...state,
    phase: text.length === 0 ? 'loaded-empty' : 'loaded-nonempty',
    text,
  };
}

export function failPipelineLogRequest(
  state: PipelineLogPanelState,
  identity: PipelineLogRequestIdentity,
  error: string,
): PipelineLogPanelState {
  if (state.phase !== 'loading' || !sameIdentity(state.identity, identity)) return state;
  return { ...state, phase: 'error', error };
}
