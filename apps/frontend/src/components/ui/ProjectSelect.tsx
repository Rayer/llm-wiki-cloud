'use client';

import { useEffect, useRef, useState } from 'react';
import { ChevronDown, Check, Search } from 'lucide-react';

export type ProjectOption = {
  id: string;
  name: string;
};

export function ProjectSelect({
  projects,
  value,
  onChange,
  placeholder,
  searchPlaceholder,
  noMatches,
  maxResults,
}: {
  projects: ProjectOption[];
  value: string;
  onChange: (projectId: string) => void;
  placeholder?: string;
  searchPlaceholder?: string;
  noMatches?: string;
  maxResults?: number;
}) {
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState('');
  const rootRef = useRef<HTMLDivElement>(null);
  const selected = projects.find((p) => p.id === value);
  const matching = search.trim()
    ? projects.filter((project) => `${project.name} ${project.id}`.toLocaleLowerCase().includes(search.trim().toLocaleLowerCase()))
    : projects;
  const visibleProjects = maxResults && !search.trim() ? matching.slice(0, maxResults) : matching;

  useEffect(() => {
    if (!open) return;
    const handleClick = (event: MouseEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) {
        setOpen(false);
      }
    };
    const handleKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setOpen(false);
    };
    document.addEventListener('mousedown', handleClick);
    document.addEventListener('keydown', handleKey);
    return () => {
      document.removeEventListener('mousedown', handleClick);
      document.removeEventListener('keydown', handleKey);
    };
  }, [open]);

  return (
    <div ref={rootRef} className="relative">
      <button
        type="button"
        onClick={() => setOpen((prev) => { if (!prev) setSearch(''); return !prev; })}
        className="flex w-full items-center justify-between gap-2 rounded border border-white/10 bg-zinc-900/60 px-3 py-2 text-left text-sm font-medium text-zinc-100 outline-none transition hover:bg-white/5 focus:border-emerald-400/50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-400"
        aria-haspopup="listbox"
        aria-expanded={open}
      >
        <span className="truncate">{selected?.name ?? placeholder ?? 'Select project'}</span>
        <ChevronDown className={`size-4 shrink-0 text-zinc-500 transition ${open ? 'rotate-180' : ''}`} />
      </button>

      {open ? (
        <div className="absolute left-0 right-0 z-20 mt-1 rounded border border-white/10 bg-zinc-900/95 p-1 shadow-xl animate-scale-in">
          <label className="flex min-h-10 items-center gap-2 border-b border-white/8 px-2 text-zinc-500">
            <Search className="size-4 shrink-0" aria-hidden="true" />
            <input autoFocus type="search" value={search} onChange={(event) => setSearch(event.target.value)} placeholder={searchPlaceholder ?? 'Search all projects'} aria-label={searchPlaceholder ?? 'Search all projects'} className="min-w-0 flex-1 bg-transparent py-2 text-sm text-zinc-100 outline-none placeholder:text-zinc-500" />
          </label>
          <ul role="listbox" className="max-h-48 overflow-y-auto p-1">
          {visibleProjects.map((project) => (
            <li key={project.id} role="option" aria-selected={project.id === value}>
              <button
                type="button"
                onClick={() => {
                  onChange(project.id);
                  setOpen(false);
                }}
                className={`flex w-full items-center justify-between gap-2 rounded-md px-3 py-2 text-left text-sm transition ${
                  project.id === value
                    ? 'bg-emerald-500/15 text-emerald-200'
                    : 'text-zinc-300 hover:bg-white/8 hover:text-white'
                }`}
              >
                <span className="truncate">{project.name}</span>
                {project.id === value ? <Check className="size-4 shrink-0" /> : null}
              </button>
            </li>
          ))}
          {visibleProjects.length === 0 ? <li className="px-3 py-3 text-sm text-zinc-500">{noMatches ?? 'No matching projects.'}</li> : null}
          </ul>
        </div>
      ) : null}
    </div>
  );
}
