'use client';

import { useEffect, useRef, useState } from 'react';
import { ChevronDown, Check } from 'lucide-react';

export type ProjectOption = {
  id: string;
  name: string;
};

export function ProjectSelect({
  projects,
  value,
  onChange,
  placeholder,
}: {
  projects: ProjectOption[];
  value: string;
  onChange: (projectId: string) => void;
  placeholder?: string;
}) {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const selected = projects.find((p) => p.id === value);

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
        onClick={() => setOpen((prev) => !prev)}
        className="flex w-full items-center justify-between gap-2 rounded-lg border border-white/10 bg-zinc-900/60 px-3 py-2 text-left text-sm font-medium text-zinc-100 outline-none transition hover:bg-white/5 focus:border-emerald-400/50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-400"
        aria-haspopup="listbox"
        aria-expanded={open}
      >
        <span className="truncate">{selected?.name ?? placeholder ?? 'Select project'}</span>
        <ChevronDown className={`size-4 shrink-0 text-zinc-500 transition ${open ? 'rotate-180' : ''}`} />
      </button>

      {open ? (
        <ul
          role="listbox"
          className="absolute left-0 right-0 z-20 mt-1 max-h-48 overflow-y-auto rounded-lg border border-white/10 bg-zinc-900/95 p-1 shadow-xl backdrop-blur-xl animate-scale-in"
        >
          {projects.map((project) => (
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
        </ul>
      ) : null}
    </div>
  );
}