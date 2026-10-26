import type { Locale } from './i18n-core';

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

export function buildMCPGuideSnippet(target: MCPGuideTarget, resourceURL: string, locale: Locale = 'en'): string {
  if (!isUsableMCPResourceURL(resourceURL)) return '';

  switch (target) {
    case 'hermes':
      return [
        'mcp_servers:',
        '  llm_wiki:',
        `    url: ${JSON.stringify(resourceURL)}`,
        '    auth: oauth',
        '    oauth:',
        '      cimd: false',
        '      scope: "mcp:query mcp:raw:write"',
      ].join('\n');
    case 'codex':
      return [
        `codex mcp add llm-wiki --url ${shellQuote(resourceURL)}`,
        'codex mcp login llm-wiki --scopes mcp:query,mcp:raw:write --oauth-client-registration dcr',
      ].join('\n');
    case 'grok-cli':
      return `grok mcp add --transport http llm-wiki ${shellQuote(resourceURL)}`;
    case 'grok-bot':
      return locale === 'zh-TW'
        ? '請加入 MCP：' + resourceURL + '，用來處理你的【請填寫此 Project 要處理的資料用途】。'
        : 'Please add MCP: ' + resourceURL + ' to handle [describe the data this Project should handle].';
    case 'openai-web':
      return locale === 'zh-TW'
        ? '我想連接一個 engineering wiki，用來存取【請填寫這個 Project 要處理的資料用途】：' + resourceURL
        : 'I want to connect an engineering wiki to access [describe the data or purpose this Project should handle]: ' + resourceURL;
    case 'claude-code':
      return `claude mcp add --transport http llm-wiki ${shellQuote(resourceURL)}`;
    case 'openclaw': {
      const definition = JSON.stringify({
        url: resourceURL,
        transport: 'streamable-http',
        auth: 'oauth',
        oauth: { scope: 'mcp:query mcp:raw:write' },
      });
      return [
        `openclaw mcp set llm-wiki ${shellQuote(definition)}`,
        'openclaw mcp login llm-wiki',
        'openclaw mcp doctor llm-wiki --probe',
      ].join('\n');
    }
  }
}
