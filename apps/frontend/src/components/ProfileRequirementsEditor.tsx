'use client';

import { useState } from 'react';
import type { ProfileRequirement } from '@/lib/api';

function newRequirementId(): string {
  return globalThis.crypto?.randomUUID?.() ?? `req-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

export function ProfileRequirementsEditor({
  requirements,
  onChange,
  disabled = false,
}: {
  requirements: ProfileRequirement[];
  onChange: (requirements: ProfileRequirement[]) => void;
  disabled?: boolean;
}) {
  const [emptyRowId, setEmptyRowId] = useState(newRequirementId);
  const rows = requirements.length > 0
    ? requirements
    : [{ id: emptyRowId, text: '' }];

  function changeText(rowId: string, text: string) {
    if (requirements.length === 0 && rowId === emptyRowId) {
      if (text) onChange([{ id: rowId, text }]);
      return;
    }
    onChange(requirements.map((item) => item.id === rowId ? { ...item, text } : item));
  }

  function move(index: number, offset: -1 | 1) {
    const target = index + offset;
    if (target < 0 || target >= requirements.length) return;
    const next = [...requirements];
    [next[index], next[target]] = [next[target], next[index]];
    onChange(next);
  }

  return (
    <div className="space-y-3">
      <ol className="space-y-3">
        {rows.map((requirement, index) => {
          const placeholder = requirements.length === 0;
          return (
            <li key={requirement.id} className="flex items-start gap-2">
              <label className="min-w-0 flex-1 text-sm text-zinc-300">
                Requirement {index + 1}
                <textarea
                  aria-label={`Requirement ${index + 1}`}
                  value={requirement.text}
                  onChange={(event) => changeText(requirement.id, event.target.value)}
                  rows={2}
                  disabled={disabled}
                  placeholder="Optional; describe what matters for this project"
                  className="mt-1.5 w-full resize-y rounded-lg border border-white/10 bg-black/30 px-3 py-2.5 text-sm leading-6 text-white outline-none transition placeholder:text-zinc-500 focus:border-emerald-300 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-400 disabled:opacity-60"
                />
              </label>
              {!placeholder ? (
                <div className="mt-6 flex shrink-0 gap-1">
                  <button
                    type="button"
                    disabled={disabled || index === 0}
                    aria-label={`Move requirement ${index + 1} up`}
                    onClick={() => move(index, -1)}
                    className="min-h-10 rounded-md px-2 text-xs text-zinc-400 hover:bg-white/10 hover:text-white disabled:opacity-40"
                  >
                    ↑
                  </button>
                  <button
                    type="button"
                    disabled={disabled || index === requirements.length - 1}
                    aria-label={`Move requirement ${index + 1} down`}
                    onClick={() => move(index, 1)}
                    className="min-h-10 rounded-md px-2 text-xs text-zinc-400 hover:bg-white/10 hover:text-white disabled:opacity-40"
                  >
                    ↓
                  </button>
                  <button
                    type="button"
                    disabled={disabled}
                    aria-label={`Remove requirement ${index + 1}`}
                    onClick={() => {
                      const next = requirements.filter((item) => item.id !== requirement.id);
                      if (next.length === 0) setEmptyRowId(newRequirementId());
                      onChange(next);
                    }}
                    className="min-h-10 rounded-md px-2 text-xs text-zinc-400 hover:bg-red-400/10 hover:text-red-200 disabled:opacity-40"
                  >
                    Remove
                  </button>
                </div>
              ) : null}
            </li>
          );
        })}
      </ol>
      <button
        type="button"
        disabled={disabled}
        onClick={() => onChange([...requirements, { id: newRequirementId(), text: '' }])}
        className="min-h-10 rounded-md border border-white/10 px-3 text-sm text-zinc-300 transition hover:border-emerald-300/40 hover:text-white disabled:opacity-50"
      >
        Add requirement
      </button>
    </div>
  );
}
