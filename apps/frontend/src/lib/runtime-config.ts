export type RuntimeConfig = {
  schema_version: 1;
  api_url: string;
  auth_url: string;
};

export type RuntimeConfigErrorKind = 'missing' | 'load' | 'format';

export class RuntimeConfigError extends Error {
  readonly kind: RuntimeConfigErrorKind;

  constructor(message: string, kind: RuntimeConfigErrorKind) {
    super(message);
    this.name = 'RuntimeConfigError';
    this.kind = kind;
  }
}

type RuntimeConfigState = {
  config: RuntimeConfig | null;
  load: Promise<RuntimeConfig> | null;
};

type RuntimeConfigGlobal = typeof globalThis & {
  __lwcRuntimeConfigState?: RuntimeConfigState;
};

const globalState = globalThis as RuntimeConfigGlobal;
const state = globalState.__lwcRuntimeConfigState ??= { config: null, load: null };
const CONFIG_LOAD_TIMEOUT_MS = 10_000;

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

function isLoopback(hostname: string): boolean {
  return hostname === 'localhost' || hostname === '127.0.0.1' || hostname === '[::1]';
}

function invalidFormat(message: string): never {
  throw new RuntimeConfigError(`Runtime config format error: ${message}.`, 'format');
}

function readEndpoint(record: Record<string, unknown>, name: 'api_url' | 'auth_url'): string {
  const value = record[name];
  if (value === undefined || value === null || (typeof value === 'string' && !value.trim())) {
    throw new RuntimeConfigError(`parameter missing: ${name}`, 'missing');
  }
  if (typeof value !== 'string') return invalidFormat(`${name} must be a URL`);

  let parsed: URL;
  try {
    parsed = new URL(value.trim());
  } catch {
    return invalidFormat(`invalid URL for ${name}`);
  }

  const localHttp = parsed.protocol === 'http:' && isLoopback(parsed.hostname);
  if (
    (parsed.protocol !== 'https:' && !localHttp)
    || parsed.username
    || parsed.password
    || parsed.search
    || parsed.hash
  ) {
    return invalidFormat(`invalid URL for ${name}`);
  }

  return parsed.href.replace(/\/+$/, '');
}

export function validateRuntimeConfig(value: unknown): RuntimeConfig {
  if (!isRecord(value)) return invalidFormat('expected a JSON object');
  if (value.schema_version !== 1) return invalidFormat('unsupported schema_version');
  if (Object.keys(value).some((key) => !['schema_version', 'api_url', 'auth_url'].includes(key))) {
    return invalidFormat('unexpected field');
  }

  return {
    schema_version: 1,
    api_url: readEndpoint(value, 'api_url'),
    auth_url: readEndpoint(value, 'auth_url'),
  };
}

function configURL(): string {
  const value = process.env.NEXT_PUBLIC_CONFIG_URL;
  if (typeof value !== 'string' || !value.trim()) {
    throw new RuntimeConfigError('parameter missing: NEXT_PUBLIC_CONFIG_URL', 'missing');
  }

  const configured = value.trim();
  let parsed: URL;
  try {
    parsed = new URL(configured, typeof window === 'undefined' ? 'http://localhost/' : window.location.href);
  } catch {
    return invalidFormat('invalid URL for NEXT_PUBLIC_CONFIG_URL');
  }

  const localHttp = parsed.protocol === 'http:' && isLoopback(parsed.hostname);
  if (
    (parsed.protocol !== 'https:' && !localHttp)
    || parsed.username
    || parsed.password
    || parsed.search
    || parsed.hash
    || configured.startsWith('//')
  ) {
    return invalidFormat('invalid URL for NEXT_PUBLIC_CONFIG_URL');
  }

  return configured;
}

async function fetchRuntimeConfig(): Promise<RuntimeConfig> {
  const url = configURL();
  const controller = new AbortController();
  let timedOut = false;
  let timeout: ReturnType<typeof setTimeout> | undefined;
  const timeoutPromise = new Promise<never>((_resolve, reject) => {
    timeout = setTimeout(() => {
      timedOut = true;
      controller.abort();
      reject(new RuntimeConfigError('Runtime config load timed out after 10 seconds.', 'load'));
    }, CONFIG_LOAD_TIMEOUT_MS);
  });
  const responsePromise = fetch(url, {
    method: 'GET',
    credentials: 'omit',
    cache: 'no-store',
    signal: controller.signal,
  }).then(async (response) => {
    if (!response.ok) {
      throw new RuntimeConfigError(`Runtime config load failed (HTTP ${response.status}).`, 'load');
    }

    let payload: unknown;
    try {
      payload = await response.json();
    } catch {
      throw new RuntimeConfigError('Runtime config format error: invalid JSON.', 'format');
    }
    return validateRuntimeConfig(payload);
  }).catch((error: unknown) => {
    if (error instanceof RuntimeConfigError) throw error;
    throw new RuntimeConfigError(
      timedOut ? 'Runtime config load timed out after 10 seconds.' : 'Runtime config load failed.',
      'load',
    );
  });

  try {
    return await Promise.race([responsePromise, timeoutPromise]);
  } finally {
    if (timeout !== undefined) clearTimeout(timeout);
  }
}

export function getRuntimeConfig(): RuntimeConfig {
  if (!state.config) throw new RuntimeConfigError('Runtime config has not been loaded.', 'load');
  return state.config;
}

export function loadRuntimeConfig(): Promise<RuntimeConfig> {
  if (state.config) return Promise.resolve(state.config);
  if (state.load) return state.load;

  state.load = fetchRuntimeConfig().then((config) => {
    state.config = config;
    return config;
  }).catch((error: unknown) => {
    state.load = null;
    throw error;
  });
  return state.load;
}

export function setRuntimeConfigForTests(value: unknown): RuntimeConfig {
  const config = validateRuntimeConfig(value);
  state.config = config;
  state.load = Promise.resolve(config);
  return config;
}

export function clearRuntimeConfigForTests(): void {
  state.config = null;
  state.load = null;
}
