'use client';

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useRouter } from 'next/navigation';
import {
  Search,
  FileText,
  Brain,
  Activity,
  FolderKanban,
  ArrowRight,
} from 'lucide-react';
import type { ProjectOption } from './ProjectSelect';

type CommandItem = {
  id: string;
  label: string;
  group: string;
  icon: React.ReactNode;
  action: () => void;
  keywords?: string[];
};

export function CommandPalette({
  open,
  onClose,
  projects,
  onSelectProject,
  labels,
}: {
  open: boolean;
  onClose: () => void;
  projects: ProjectOption[];
  onSelectProject: (id: string) => void;
  labels: {
    placeholder: string;
    navigate: string;
    projects: string;
    search: string;
  };
}) {
  const router = useRouter();
  const inputRef = useRef<HTMLInputElement>(null);
  const [query, setQuery] = useState('');
  const [activeIndex, setActiveIndex] = useState(0);

  useEffect(() => {
    if (!open) return;
    inputRef.current?.focus();
  }, [open]);

  const handleClose = useCallback(() => {
    setQuery('');
    setActiveIndex(0);
    onClose();
  }, [onClose]);

  const items = useMemo<CommandItem[]>(() => {
    const nav: CommandItem[] = [
      {
        id: 'nav-search',
        label: labels.search,
        group: labels.navigate,
        icon: <Search className="size-4" />,
        action: () => router.push('/'),
        keywords: ['home', 'search'],
      },
      {
        id: 'nav-sources',
        label: 'Sources',
        group: labels.navigate,
        icon: <FileText className="size-4" />,
        action: () => router.push('/sources'),
        keywords: ['source', 'documents'],
      },
      {
        id: 'nav-concepts',
        label: 'Concepts',
        group: labels.navigate,
        icon: <Brain className="size-4" />,
        action: () => router.push('/concepts'),
        keywords: ['concept', 'wiki'],
      },
      {
        id: 'nav-status',
        label: 'Status',
        group: labels.navigate,
        icon: <Activity className="size-4" />,
        action: () => router.push('/status'),
        keywords: ['pipeline', 'status'],
      },
    ];

    const projectItems: CommandItem[] = projects.map((project) => ({
      id: `project-${project.id}`,
      label: project.name,
      group: labels.projects,
      icon: <FolderKanban className="size-4" />,
      action: () => onSelectProject(project.id),
      keywords: [project.id],
    }));

    const q = query.trim();
    const searchItem: CommandItem[] = q
      ? [{
          id: 'search-query',
          label: `Search "${q}"`,
          group: 'Search',
          icon: <ArrowRight className="size-4" />,
          action: () => router.push(`/?q=${encodeURIComponent(q)}`),
          keywords: [q],
        }]
      : [];

    return [...searchItem, ...nav, ...projectItems];
  }, [labels, onSelectProject, projects, query, router]);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return items;
    return items.filter((item) => {
      const haystack = [item.label, item.group, ...(item.keywords ?? [])].join(' ').toLowerCase();
      return haystack.includes(q);
    });
  }, [items, query]);

  const runActive = useCallback(() => {
    const item = filtered[activeIndex];
    if (!item) return;
    item.action();
    handleClose();
  }, [activeIndex, filtered, handleClose]);

  useEffect(() => {
    if (!open) return;
    const handler = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        handleClose();
        return;
      }
      if (event.key === 'ArrowDown') {
        event.preventDefault();
        setActiveIndex((i) => Math.min(i + 1, filtered.length - 1));
      }
      if (event.key === 'ArrowUp') {
        event.preventDefault();
        setActiveIndex((i) => Math.max(i - 1, 0));
      }
      if (event.key === 'Enter') {
        event.preventDefault();
        runActive();
      }
    };
    window.addEventListener('keydown', handler);
    return () => window.removeEventListener('keydown', handler);
  }, [activeIndex, filtered.length, handleClose, open, runActive]);

  if (!open) return null;

  const groups = [...new Set(filtered.map((item) => item.group))];

  return (
    <div
      className="fixed inset-0 z-[60] flex items-start justify-center bg-black/60 p-4 pt-[15vh] backdrop-blur-sm animate-fade-in"
      onClick={handleClose}
    >
      <div
        className="w-full max-w-lg overflow-hidden rounded-xl border border-white/10 bg-zinc-900/95 shadow-2xl backdrop-blur-xl animate-scale-in"
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-label="Command palette"
      >
        <div className="flex items-center gap-3 border-b border-white/10 px-4">
          <Search className="size-4 text-zinc-500" />
          <input
            ref={inputRef}
            value={query}
            onChange={(e) => {
              setQuery(e.target.value);
              setActiveIndex(0);
            }}
            placeholder={labels.placeholder}
            className="flex-1 bg-transparent py-4 text-sm text-white outline-none placeholder:text-zinc-600"
          />
          <kbd className="hidden rounded border border-white/10 bg-white/5 px-1.5 py-0.5 text-[10px] text-zinc-500 sm:inline">
            ESC
          </kbd>
        </div>

        <div className="max-h-80 overflow-y-auto p-2">
          {filtered.length === 0 ? (
            <p className="px-3 py-6 text-center text-sm text-zinc-500">No matches</p>
          ) : (
            groups.map((group) => {
              const groupItems = filtered
                .map((item, index) => ({ item, index }))
                .filter(({ item }) => item.group === group);
              return (
                <div key={group} className="mb-2">
                  <p className="px-3 py-1 text-[10px] font-semibold uppercase tracking-wider text-zinc-600">
                    {group}
                  </p>
                  {groupItems.map(({ item, index }) => (
                    <button
                      key={item.id}
                      type="button"
                      onMouseEnter={() => setActiveIndex(index)}
                      onClick={() => {
                        item.action();
                        handleClose();
                      }}
                      className={`flex w-full items-center gap-3 rounded-lg px-3 py-2.5 text-left text-sm transition ${
                        index === activeIndex
                          ? 'bg-emerald-500/15 text-emerald-100'
                          : 'text-zinc-300 hover:bg-white/8 hover:text-white'
                      }`}
                    >
                      <span className="text-zinc-500">{item.icon}</span>
                      <span className="flex-1 truncate">{item.label}</span>
                    </button>
                  ))}
                </div>
              );
            })
          )}
        </div>
      </div>
    </div>
  );
}

export function useCommandPalette() {
  const [open, setOpen] = useState(false);

  useEffect(() => {
    const handler = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault();
        setOpen((prev) => !prev);
      }
    };
    window.addEventListener('keydown', handler);
    return () => window.removeEventListener('keydown', handler);
  }, []);

  return { open, setOpen };
}