'use client';

import type { ReactNode } from 'react';

// Track current markdown section for wikilink routing
let currentWikilinkSection: 'sources' | 'concepts' = 'concepts';

export function MarkdownView({ content }: { content?: string }) {
  if (!content) {
    return (
      <div className="rounded-md border border-white/10 bg-[#111111] p-6 text-zinc-400">
        No markdown content was returned for this entry.
      </div>
    );
  }

  // Strip leading "# Title" to avoid double title with page header
  const clean = content.replace(/^# .+\n\n?/, '').replace(/^.+\n=+\n\n?/, '').trimStart();

  return (
    <article className="markdown-body">
      {renderMarkdown(clean)}
    </article>
  );
}

function renderMarkdown(content: string) {
  const lines = content.split(/\r?\n/);
  const nodes: ReactNode[] = [];
  let index = 0;

  while (index < lines.length) {
    const line = lines[index];

    if (!line.trim()) {
      index += 1;
      continue;
    }

    if (/^```/.test(line)) {
      const language = line.replace(/^```/, '').trim();
      const code: string[] = [];
      index += 1;
      while (index < lines.length && !/^```/.test(lines[index])) {
        code.push(lines[index]);
        index += 1;
      }
      index += 1;
      nodes.push(
        <pre key={nodes.length}>
          <code className={language ? `language-${language}` : undefined}>
            {code.join('\n')}
          </code>
        </pre>,
      );
      continue;
    }

    const heading = /^(#{1,4})\s+(.+)$/.exec(line);
    if (heading) {
      const level = heading[1].length;
      const headingText = heading[2];
      // Track section for wikilink routing
      if (level === 2) {
        const lc = headingText.toLowerCase();
        if (lc === 'sources') currentWikilinkSection = 'sources';
        else if (lc === 'concepts') currentWikilinkSection = 'concepts';
      }
      const children = renderInline(headingText);
      const key = nodes.length;
      if (level === 1) nodes.push(<h1 key={key}>{children}</h1>);
      if (level === 2) nodes.push(<h2 key={key}>{children}</h2>);
      if (level === 3) nodes.push(<h3 key={key}>{children}</h3>);
      if (level === 4) nodes.push(<h4 key={key}>{children}</h4>);
      index += 1;
      continue;
    }

    if (/^>\s?/.test(line)) {
      const quote: string[] = [];
      while (index < lines.length && /^>\s?/.test(lines[index])) {
        quote.push(lines[index].replace(/^>\s?/, ''));
        index += 1;
      }
      nodes.push(<blockquote key={nodes.length}>{quote.map(renderInline)}</blockquote>);
      continue;
    }

    if (isTableStart(lines, index)) {
      const tableLines: string[] = [];
      while (index < lines.length && lines[index].includes('|')) {
        tableLines.push(lines[index]);
        index += 1;
      }
      nodes.push(renderTable(tableLines, nodes.length));
      continue;
    }

    if (/^\s*[-*]\s+/.test(line)) {
      const items: string[] = [];
      while (index < lines.length && /^\s*[-*]\s+/.test(lines[index])) {
        items.push(lines[index].replace(/^\s*[-*]\s+/, ''));
        index += 1;
      }
      nodes.push(
        <ul key={nodes.length}>
          {items.map((item, itemIndex) => (
            <li key={itemIndex}>{renderInline(item)}</li>
          ))}
        </ul>,
      );
      continue;
    }

    if (/^\s*\d+\.\s+/.test(line)) {
      const items: string[] = [];
      while (index < lines.length && /^\s*\d+\.\s+/.test(lines[index])) {
        items.push(lines[index].replace(/^\s*\d+\.\s+/, ''));
        index += 1;
      }
      nodes.push(
        <ol key={nodes.length}>
          {items.map((item, itemIndex) => (
            <li key={itemIndex}>{renderInline(item)}</li>
          ))}
        </ol>,
      );
      continue;
    }

    const paragraph = [line.trim()];
    index += 1;
    while (
      index < lines.length &&
      lines[index].trim() &&
      !/^(#{1,4})\s+/.test(lines[index]) &&
      !/^```/.test(lines[index]) &&
      !/^\s*[-*]\s+/.test(lines[index]) &&
      !/^\s*\d+\.\s+/.test(lines[index])
    ) {
      paragraph.push(lines[index].trim());
      index += 1;
    }
    nodes.push(<p key={nodes.length}>{renderInline(paragraph.join(' '))}</p>);
  }

  return nodes;
}

function renderInline(text: string): ReactNode[] {
  const parts = text.split(/(`[^`]+`|\*\*[^*]+\*\*|\[\[[^\]]+\]\]|\[[^\]]+\]\([^)]+\))/g);

  return parts.map((part, index) => {
    if (part.startsWith('`') && part.endsWith('`')) {
      return <code key={index}>{part.slice(1, -1)}</code>;
    }
    if (part.startsWith('**') && part.endsWith('**')) {
      return <strong key={index}>{part.slice(2, -2)}</strong>;
    }
    // Obsidian-style wikilink: [[Page Name]] — route based on section context
    const wikilink = /^\[\[([^\]]+)\]\]$/.exec(part);
    if (wikilink) {
      const collection = currentWikilinkSection === 'sources' ? 'sources' : 'concepts';
      return (
        <a key={index} href={`/${collection}/${encodeURIComponent(wikilink[1])}`} className="text-emerald-300 underline hover:text-emerald-200">
          {wikilink[1]}
        </a>
      );
    }
    const link = /^\[([^\]]+)\]\(([^)]+)\)$/.exec(part);
    if (link) {
      return (
        <a key={index} href={link[2]} target="_blank" rel="noreferrer">
          {link[1]}
        </a>
      );
    }
    return part;
  });
}

function isTableStart(lines: string[], index: number) {
  return (
    lines[index]?.includes('|') &&
    index + 1 < lines.length &&
    /^\s*\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)+\|?\s*$/.test(lines[index + 1])
  );
}

function renderTable(lines: string[], key: number) {
  const [headerLine, , ...bodyLines] = lines;
  const headers = splitTableRow(headerLine);

  return (
    <table key={key}>
      <thead>
        <tr>
          {headers.map((header, index) => (
            <th key={index}>{renderInline(header)}</th>
          ))}
        </tr>
      </thead>
      <tbody>
        {bodyLines.map((row, rowIndex) => (
          <tr key={rowIndex}>
            {splitTableRow(row).map((cell, cellIndex) => (
              <td key={cellIndex}>{renderInline(cell)}</td>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function splitTableRow(line: string) {
  return line
    .trim()
    .replace(/^\|/, '')
    .replace(/\|$/, '')
    .split('|')
    .map((cell) => cell.trim());
}
