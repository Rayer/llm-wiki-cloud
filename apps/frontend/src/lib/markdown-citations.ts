type TextNodePosition = {
  start?: { offset?: number };
  end?: { offset?: number };
};

type PositionedTextNode = {
  value: string;
  position?: TextNodePosition;
};

type RawSpan = {
  start: number;
  end: number;
  unsafe: boolean;
};

const escapedPunctuation = "!\"#$%&'()*+,-./:;<=>?@[\\\\]^_`{|}~";
const namedCharacterReferences: Record<string, string> = {
  amp: '&',
  apos: "'",
  gt: '>',
  lbrack: '[',
  lsqb: '[',
  lt: '<',
  nbsp: '\u00a0',
  quot: '"',
  rbrack: ']',
  rsqb: ']',
};

function decodeCharacterReference(value: string): string | null {
  const numeric = /^&#(?:x([\da-f]+)|([\d]+));$/i.exec(value);
  if (numeric) {
    const codePoint = Number.parseInt(numeric[1] ?? numeric[2], numeric[1] ? 16 : 10);
    return Number.isInteger(codePoint) && codePoint >= 0 && codePoint <= 0x10ffff
      ? String.fromCodePoint(codePoint)
      : null;
  }

  const named = /^&([a-z][a-z\d]+);$/i.exec(value);
  return named ? namedCharacterReferences[named[1].toLowerCase()] ?? null : null;
}

function mapCookedTextToRaw(node: PositionedTextNode, content: string): RawSpan[] | null {
  const start = node.position?.start?.offset;
  const end = node.position?.end?.offset;
  if (typeof start !== 'number' || typeof end !== 'number' || !Number.isInteger(start) || !Number.isInteger(end) || start < 0 || end < start) return null;

  const raw = content.slice(start, end);
  const spans: RawSpan[] = [];
  let rawIndex = 0;
  let cookedIndex = 0;

  while (rawIndex < raw.length) {
    if (raw[rawIndex] === '\\' && escapedPunctuation.includes(raw[rawIndex + 1] ?? '')) {
      if (node.value[cookedIndex] !== raw[rawIndex + 1]) return null;
      spans.push({ start: rawIndex, end: rawIndex + 2, unsafe: true });
      rawIndex += 2;
      cookedIndex += 1;
      continue;
    }

    if (raw[rawIndex] === '&') {
      const entityEnd = raw.indexOf(';', rawIndex + 1);
      if (entityEnd !== -1) {
        const entity = raw.slice(rawIndex, entityEnd + 1);
        const decoded = decodeCharacterReference(entity);
        if (decoded === null || node.value.slice(cookedIndex, cookedIndex + decoded.length) !== decoded) return null;
        for (let index = 0; index < decoded.length; index += 1) {
          spans.push({ start: rawIndex, end: entityEnd + 1, unsafe: true });
        }
        rawIndex = entityEnd + 1;
        cookedIndex += decoded.length;
        continue;
      }
    }

    if (node.value[cookedIndex] !== raw[rawIndex]) return null;
    spans.push({ start: rawIndex, end: rawIndex + 1, unsafe: false });
    rawIndex += 1;
    cookedIndex += 1;
  }

  return cookedIndex === node.value.length ? spans : null;
}

/** Return a citation's exact raw source range, or null when decoding makes it ambiguous. */
export function getExactRawCitationRange(
  node: PositionedTextNode,
  content: string,
  cookedStart: number,
  cookedEnd: number,
): { start: number; end: number } | null {
  const spans = mapCookedTextToRaw(node, content);
  if (!spans || cookedStart < 0 || cookedEnd > spans.length || cookedStart >= cookedEnd) return null;

  const selected = spans.slice(cookedStart, cookedEnd);
  if (selected.some((span) => span.unsafe)) return null;

  const rawStart = selected[0].start;
  const rawEnd = selected[selected.length - 1].end;
  const nodeStart = node.position?.start?.offset;
  if (typeof nodeStart !== 'number' || !Number.isInteger(nodeStart)) return null;
  if (content[nodeStart + rawStart - 1] === '\\') return null;

  return { start: nodeStart + rawStart, end: nodeStart + rawEnd };
}
