'use client';

interface Props {
  open: boolean;
  onClose: () => void;
}

export function ComingSoonModal({ open, onClose }: Props) {
  if (!open) return null;
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/80 p-4 backdrop-blur-sm">
      <div className="w-full max-w-sm rounded-2xl border border-white/10 bg-[#151515] p-6 shadow-2xl text-center">
        <h2 className="text-xl font-semibold text-white">Coming Soon</h2>
        <p className="mt-3 text-sm text-zinc-400">
          Registration will be available in a future update.
        </p>
        <button
          type="button"
          onClick={onClose}
          className="mt-6 w-full rounded-lg bg-emerald-300 px-4 py-3 font-semibold text-black transition hover:bg-emerald-200"
        >
          OK
        </button>
      </div>
    </div>
  );
}
