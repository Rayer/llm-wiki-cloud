export type CLIReleaseAsset = {
  os: 'darwin' | 'windows' | 'linux';
  arch: 'arm64' | 'amd64';
  asset_name: string;
  size: number;
  sha256: string;
  download_url?: string;
};

export type CLIReleaseManifest = {
  schema_version: 1;
  version: string;
  source_commit: string;
  channel: 'stable';
  assets: CLIReleaseAsset[];
};

const expectedTargets = new Set(['darwin/arm64', 'darwin/amd64', 'windows/amd64', 'linux/amd64']);
const semverPattern = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9]\d*|\d*[A-Za-z-][0-9A-Za-z-]*))*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/;
const sha256Pattern = /^[0-9a-f]{64}$/;
const gitSHA1Pattern = /^[0-9a-f]{40}$/;

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

function metadataURL(): string {
  const value = process.env.NEXT_PUBLIC_CLI_RELEASE_METADATA_URL?.trim();
  if (!value) return '';
  const parsed = new URL(value, typeof window === 'undefined' ? 'http://localhost/' : window.location.href);
  const localHttp = parsed.protocol === 'http:' && ['localhost', '127.0.0.1', '[::1]'].includes(parsed.hostname);
  if ((parsed.protocol !== 'https:' && !localHttp) || parsed.username || parsed.password || parsed.search || parsed.hash || value.startsWith('//')) {
    throw new Error('Invalid CLI release metadata URL.');
  }
  return parsed.href;
}

export function validateCLIReleaseManifest(value: unknown, endpoint: string): CLIReleaseManifest {
  if (!isRecord(value) || value.schema_version !== 1 || value.channel !== 'stable') {
    throw new Error('Unsupported CLI release metadata.');
  }
  if (typeof value.version !== 'string' || !semverPattern.test(value.version) || value.version.split('+', 1)[0].includes('-')) {
    throw new Error('Invalid stable CLI version.');
  }
  if (typeof value.source_commit !== 'string' || !gitSHA1Pattern.test(value.source_commit)) {
    throw new Error('Invalid CLI source commit.');
  }
  if (!Array.isArray(value.assets) || value.assets.length !== expectedTargets.size) {
    throw new Error('Incomplete CLI platform metadata.');
  }
  const metadataURL = new URL(endpoint, typeof window === 'undefined' ? 'http://localhost/' : window.location.href);
  const metadataOrigin = metadataURL.origin;
  const metadataPath = metadataURL.pathname;
  const metadataSuffix = '/cli/stable/latest.json';
  if (!metadataPath.endsWith(metadataSuffix)) throw new Error('Invalid CLI release metadata endpoint.');
  const metadataBasePath = metadataPath.slice(0, -metadataSuffix.length);
  const seen = new Set<string>();
  const assets = value.assets.map((entry): CLIReleaseAsset => {
    if (!isRecord(entry) || typeof entry.os !== 'string' || typeof entry.arch !== 'string') {
      throw new Error('Invalid CLI platform metadata.');
    }
    const target = `${entry.os}/${entry.arch}`;
    if (!expectedTargets.has(target) || seen.has(target)) throw new Error('Duplicate or unsupported CLI platform.');
    seen.add(target);
    const extension = entry.os === 'windows' ? '.zip' : '.tar.gz';
    if (typeof entry.asset_name !== 'string' || entry.asset_name !== `lwc-sync_${value.version}_${entry.os}_${entry.arch}${extension}`) {
      throw new Error('Invalid CLI asset name.');
    }
    if (!Number.isSafeInteger(entry.size) || (entry.size as number) <= 0 || typeof entry.sha256 !== 'string' || !sha256Pattern.test(entry.sha256)) {
      throw new Error('Invalid CLI asset size or checksum.');
    }
    let downloadUrl: string | undefined;
    if (entry.download_url !== undefined) {
      if (typeof entry.download_url !== 'string') throw new Error('Invalid CLI download URL.');
      const parsed = new URL(entry.download_url);
      const expectedPath = `${metadataBasePath}/cli/versions/${value.version}/${entry.asset_name}`;
      if (parsed.protocol !== 'https:' || parsed.origin !== metadataOrigin || parsed.pathname !== expectedPath || parsed.username || parsed.password || parsed.search || parsed.hash) {
        throw new Error('Untrusted CLI download URL.');
      }
      downloadUrl = parsed.href;
    }
    return {
      os: entry.os as CLIReleaseAsset['os'],
      arch: entry.arch as CLIReleaseAsset['arch'],
      asset_name: entry.asset_name,
      size: entry.size as number,
      sha256: entry.sha256,
      ...(downloadUrl ? { download_url: downloadUrl } : {}),
    };
  });
  return {
    schema_version: 1,
    version: value.version,
    source_commit: value.source_commit,
    channel: 'stable',
    assets,
  };
}

export async function loadCLIReleaseManifest(fetcher: typeof fetch = fetch): Promise<CLIReleaseManifest | null> {
  let endpoint: string;
  try {
    endpoint = metadataURL();
  } catch {
    return null;
  }
  if (!endpoint) return null;

  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 5000);
  try {
    const response = await fetcher(endpoint, {
      method: 'GET',
      credentials: 'omit',
      cache: 'no-store',
      signal: controller.signal,
    });
    if (!response.ok) return null;
    return validateCLIReleaseManifest(await response.json(), endpoint);
  } catch {
    return null;
  } finally {
    clearTimeout(timeout);
  }
}
