'use client';

import Link from 'next/link';
import { useEffect, useState, type ReactNode } from 'react';

import {
  INLINE_TOKEN_REGEX,
  normalizeWikilinkAnnotations,
  parseWikilinkToken,
} from '../lib/markdown-inline';
import { parseMarkdownImage } from '../lib/markdown-images';
import { resolveWikilink, type WikilinkSection } from '../lib/wikilinks';
import { Surface } from './ui/Surface';

// Track current markdown section for wikilink routing
let currentWikilinkSection: WikilinkSection = 'concepts';

export function MarkdownView({
  content,
  existingConceptSlugs,
}: {
  content?: string;
  existingConceptSlugs?: Set<string>;
}) {
  const [deadLinkSlug, setDeadLinkSlug] = useState<string | null>(null);

  useEffect(() => {
    if (!deadLinkSlug) return;
    const handler = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setDeadLinkSlug(null);
    };
    window.addEventListener('keydown', handler);
    return () => window.removeEventListener('keydown', handler);
  }, [deadLinkSlug]);

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
    <>
      <article className="markdown-body">
        {renderMarkdown(clean, existingConceptSlugs, setDeadLinkSlug)}
      </article>
      {deadLinkSlug ? (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4 backdrop-blur-sm"
          onClick={() => setDeadLinkSlug(null)}
        >
          <Surface
            variant="elevated"
            className="relative w-full max-w-md p-6"
            onClick={(event) => event.stopPropagation()}
          >
            <button
              type="button"
              onClick={() => setDeadLinkSlug(null)}
              className="absolute right-4 top-4 rounded-md p-1 text-zinc-400 transition hover:bg-white/10 hover:text-white"
              aria-label="Close"
            >
              <svg xmlns="http://www.w3.org/2000/svg" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                <line x1="18" y1="6" x2="6" y2="18" /><line x1="6" y1="6" x2="18" y2="18" />
              </svg>
            </button>
            <p className="pr-8 text-lg text-white">此 concept 尚不存在：{deadLinkSlug}</p>
          </Surface>
        </div>
      ) : null}
    </>
  );
}

function renderMarkdown(
  content: string,
  existingConceptSlugs: Set<string> | undefined,
  onDeadLink: (slug: string) => void,
) {
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
      const children = renderInline(headingText, existingConceptSlugs, onDeadLink);
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
      nodes.push(
        <blockquote key={nodes.length}>
          {quote.map((item) => renderInline(item, existingConceptSlugs, onDeadLink))}
        </blockquote>,
      );
      continue;
    }

    if (isTableStart(lines, index)) {
      const tableLines: string[] = [];
      while (index < lines.length && lines[index].includes('|')) {
        tableLines.push(lines[index]);
        index += 1;
      }
      nodes.push(renderTable(tableLines, nodes.length, existingConceptSlugs, onDeadLink));
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
            <li key={itemIndex}>{renderInline(item, existingConceptSlugs, onDeadLink)}</li>
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
            <li key={itemIndex}>{renderInline(item, existingConceptSlugs, onDeadLink)}</li>
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
    nodes.push(
      <p key={nodes.length}>{renderInline(paragraph.join(' '), existingConceptSlugs, onDeadLink)}</p>,
    );
  }

  return nodes;
}

function renderInline(
  text: string,
  existingConceptSlugs: Set<string> | undefined,
  onDeadLink: (slug: string) => void,
): ReactNode[] {
  const parts = normalizeWikilinkAnnotations(text).split(INLINE_TOKEN_REGEX);

  return parts.map((part, index) => {
    if (part.startsWith('`') && part.endsWith('`')) {
      return <code key={index}>{part.slice(1, -1)}</code>;
    }
    if (part.startsWith('**') && part.endsWith('**')) {
      // Recurse so nested wikilinks/links/code inside **bold** still parse (LWC-107)
      return (
        <strong key={index}>
          {renderInline(part.slice(2, -2), existingConceptSlugs, onDeadLink)}
        </strong>
      );
    }
    // Image: ![alt](url) — must be checked before regular link
    const image = parseMarkdownImage(part);
    if (image) {
      return (
        <img
          key={index}
          src={image.src}
          alt={image.alt}
          className="rounded-lg"
          loading="lazy"
        />
      );
    }
    // Obsidian-style wikilink: [[Page Name]] — route based on section context
    const wikilinkLabel = parseWikilinkToken(part);
    if (wikilinkLabel) {
      const resolved = resolveWikilink(wikilinkLabel, currentWikilinkSection, existingConceptSlugs);
      if (resolved.dead) {
        return (
          <span
            key={index}
            role="button"
            tabIndex={0}
            className="text-red-400 cursor-pointer underline"
            onClick={() => onDeadLink(resolved.label)}
            onKeyDown={(event) => {
              if (event.key === 'Enter' || event.key === ' ') {
                event.preventDefault();
                onDeadLink(resolved.label);
              }
            }}
          >
            {resolved.label}
          </span>
        );
      }
      return (
        <Link key={index} href={resolved.href!} className="text-emerald-300 underline hover:text-emerald-200">
          {resolved.label}
        </Link>
      );
    }
    const link = /^\[([^\]]+)\]\(([^)]+)\)$/.exec(part);
    if (link) {
      const href = link[2];
      const isExternal = /^https?:/.test(href);
      return (
        <a key={index} href={href} {...(isExternal ? { target: '_blank', rel: 'noreferrer' } : {})}>
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

function renderTable(
  lines: string[],
  key: number,
  existingConceptSlugs: Set<string> | undefined,
  onDeadLink: (slug: string) => void,
) {
  const [headerLine, , ...bodyLines] = lines;
  const headers = splitTableRow(headerLine);

  return (
    <table key={key}>
      <thead>
        <tr>
          {headers.map((header, index) => (
            <th key={index}>{renderInline(header, existingConceptSlugs, onDeadLink)}</th>
          ))}
        </tr>
      </thead>
      <tbody>
        {bodyLines.map((row, rowIndex) => (
          <tr key={rowIndex}>
            {splitTableRow(row).map((cell, cellIndex) => (
              <td key={cellIndex}>{renderInline(cell, existingConceptSlugs, onDeadLink)}</td>
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