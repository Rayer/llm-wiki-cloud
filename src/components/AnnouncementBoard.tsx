'use client';

import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';

const SAFE_LINK = /^(?:https?:|mailto:)/i;
const SAFE_IMAGE = /^https:\/\//i;

export function AnnouncementBoard({ markdown }: { markdown?: string | null }) {
  if (!markdown?.trim()) return null;

  return (
    <section
      aria-label="Announcement"
      className="announcement-board mt-5 max-h-52 overflow-y-auto rounded-lg border border-emerald-300/20 bg-emerald-300/5 px-4 py-3"
    >
      <div className="markdown-body announcement-markdown">
        <ReactMarkdown
          remarkPlugins={[remarkGfm]}
          urlTransform={(url) => SAFE_LINK.test(url) ? url : ''}
          components={{
            a: ({ href, children }) => SAFE_LINK.test(href ?? '') ? (
              <a href={href} target="_blank" rel="noopener noreferrer">{children}</a>
            ) : <span>{children}</span>,
            img: ({ src, alt }) => {
              const label = alt ?? '';
              const source = typeof src === 'string' ? src : '';
              if (!SAFE_IMAGE.test(source)) return null;
              // eslint-disable-next-line @next/next/no-img-element -- direct external HTTPS images are part of the announcement contract
              return <img src={source} alt={label} loading="lazy" decoding="async" referrerPolicy="no-referrer" />;
            },
          }}
        >
          {markdown}
        </ReactMarkdown>
      </div>
    </section>
  );
}
