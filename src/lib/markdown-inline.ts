// Inline markdown token regex — wikilink uses non-greedy match (LWC-134)
export const INLINE_TOKEN_REGEX =
  /(`[^`]+`|\*\*[^*]+\*\*|!\[[^\]]*\]\(.+\)|\[\[[^\]]+?\]\]|\[[^\]]+\]\([^)]+\))/g;

/** Fix [[label)]](annotation) typos before inline parsing. */
export function normalizeWikilinkAnnotations(text: string): string {
  return text.replace(/\[\[([^\]]+)\)\]\](?=\()/g, '[[$1]]');
}

export function splitInlineTokens(text: string): string[] {
  return text.split(INLINE_TOKEN_REGEX);
}

export function parseWikilinkToken(part: string): string | null {
  const match = /^\[\[([^\]]+)\]\]$/.exec(part);
  return match ? match[1] : null;
}

type WikilinkSection = 'sources' | 'concepts';

/** Convert [[wikilinks]] to markdown links for ReactMarkdown rendering. */
export function resolveWikilinksInMarkdown(md: string): string {
  const lines = normalizeWikilinkAnnotations(md).split('\n');
  let section: WikilinkSection = 'concepts';
  const out: string[] = [];

  for (const line of lines) {
    const secMatch = /^## (Sources|Concepts)/i.exec(line);
    if (secMatch) {
      section = secMatch[1].toLowerCase() as WikilinkSection;
    }

    out.push(
      line.replace(/\[\[([^\]]+)\]\]/g, (_, name: string) => {
        const parts = name.split('|');
        const slug = parts[0].trim();
        const display = (parts[1] || parts[0]).trim();
        return `[${display}](/${section}/${encodeURIComponent(slug)})`;
      }),
    );
  }

  return out.join('\n');
}