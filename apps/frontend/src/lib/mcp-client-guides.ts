import type { Locale } from './i18n-core';
import { translate } from './i18n';

export type MCPGuideTarget = 'hermes' | 'codex' | 'grok-cli' | 'grok-bot' | 'claude-code' | 'openclaw' | 'openai-web';

export function isUsableMCPResourceURL(value: string): boolean {
  try {
    const url = new URL(value);
    return ['http:', 'https:'].includes(url.protocol)
      && Boolean(url.host)
      && !url.username
      && !url.password
      && !url.hash;
  } catch {
    return false;
  }
}

function shellQuote(value: string): string {
  return `'${value.replaceAll("'", "'\\''")}'`;
}

export function buildMCPGuideSnippet(
  target: MCPGuideTarget,
  resourceURL: string,
  locale: Locale = 'en',
  scopes: readonly string[] = ['mcp:query'],
): string {
  if (!isUsableMCPResourceURL(resourceURL)) return '';
  const requestedScopes = scopes.length ? scopes.join(' ') : 'mcp:query';
  const codexScopes = requestedScopes.replaceAll(' ', ',');

  switch (target) {
    case 'hermes':
      return [
        'mcp_servers:',
        '  llm_wiki:',
        `    url: ${JSON.stringify(resourceURL)}`,
        '    auth: oauth',
        '    oauth:',
        '      cimd: false',
        `      scope: ${JSON.stringify(requestedScopes)}`,
      ].join('\n');
    case 'codex':
      return [
        `codex mcp add llm-wiki --url ${shellQuote(resourceURL)}`,
        `codex mcp login llm-wiki --scopes ${codexScopes} --oauth-client-registration dcr`,
      ].join('\n');
    case 'grok-cli':
      return `grok mcp add --transport http llm-wiki ${shellQuote(resourceURL)}\n# ${translate(locale, 'MCPOAuth.scopeSnippetNote', { scopes: requestedScopes })}`;
    case 'grok-bot':
      return translate(locale, 'MCPOAuth.shortPromptGrokBot', { resourceURL, scopes: requestedScopes });
    case 'openai-web':
      return translate(locale, 'MCPOAuth.shortPromptOpenAIWeb', { resourceURL, scopes: requestedScopes });
    case 'claude-code':
      return `claude mcp add --transport http llm-wiki ${shellQuote(resourceURL)}\n# ${translate(locale, 'MCPOAuth.scopeSnippetNote', { scopes: requestedScopes })}`;
    case 'openclaw': {
      const definition = JSON.stringify({
        url: resourceURL,
        transport: 'streamable-http',
        auth: 'oauth',
        oauth: { scope: requestedScopes },
      });
      return [
        `openclaw mcp set llm-wiki ${shellQuote(definition)}`,
        'openclaw mcp login llm-wiki',
        'openclaw mcp doctor llm-wiki --probe',
      ].join('\n');
    }
  }
}
