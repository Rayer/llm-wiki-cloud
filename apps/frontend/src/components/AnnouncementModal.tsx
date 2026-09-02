'use client';

import { useEffect, useRef } from 'react';
import { AnnouncementContent } from './AnnouncementBoard';

type Props = {
  markdown: string;
  title: string;
  closeLabel: string;
  dismissLabel: string;
  checked: boolean;
  onCheckedChange: (checked: boolean) => void;
  onClose: () => void;
};

export function AnnouncementModal({ markdown, title, closeLabel, dismissLabel, checked, onCheckedChange, onClose }: Props) {
  const dialogRef = useRef<HTMLDivElement>(null);
  const closeRef = useRef<HTMLButtonElement>(null);
  const onCloseRef = useRef(onClose);

  useEffect(() => {
    onCloseRef.current = onClose;
  }, [onClose]);

  useEffect(() => {
    closeRef.current?.focus();
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        onCloseRef.current();
        return;
      }
      if (event.key !== 'Tab' || !dialogRef.current) return;
      const focusable = Array.from(dialogRef.current.querySelectorAll<HTMLElement>('button, input, a[href]'));
      if (!focusable.length) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, []);

  return (
    <div className="fixed inset-0 z-[70] flex items-center justify-center bg-black/80 p-3 backdrop-blur-sm sm:p-6" onClick={onClose}>
      <div ref={dialogRef} role="dialog" aria-modal="true" aria-labelledby="announcement-title" className="flex max-h-[calc(100dvh-1.5rem)] w-full max-w-5xl flex-col overflow-hidden rounded-2xl border border-emerald-300/20 bg-[#151515] shadow-2xl sm:max-h-[calc(100dvh-3rem)]" onClick={(event) => event.stopPropagation()}>
        <div className="flex items-start justify-between gap-4 border-b border-white/10 px-5 py-4 sm:px-8">
          <h2 id="announcement-title" className="text-2xl font-semibold text-white">{title}</h2>
          <button ref={closeRef} type="button" aria-label={closeLabel} onClick={onClose} className="inline-flex min-h-11 min-w-11 items-center justify-center rounded-lg border border-white/10 text-2xl leading-none text-zinc-300 hover:bg-white/10 focus-visible:outline focus-visible:outline-2 focus-visible:outline-emerald-400">×</button>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto px-5 py-3 sm:px-8 sm:py-5">
          <AnnouncementContent markdown={markdown} />
        </div>
        <label className="flex items-center gap-3 border-t border-white/10 px-5 py-4 text-sm text-zinc-300 sm:px-8">
          <input type="checkbox" checked={checked} onChange={(event) => onCheckedChange(event.target.checked)} aria-label={dismissLabel} className="size-5 rounded border-white/20 bg-black/30 text-emerald-400 focus:ring-emerald-400" />
          {dismissLabel}
        </label>
      </div>
    </div>
  );
}
