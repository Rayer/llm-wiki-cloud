export function LoadingState({ label = 'Loading wiki data' }: { label?: string }) {
  return (
    <div className="rounded-md border border-white/10 bg-white/[0.03] p-6 text-zinc-300">
      {label}...
    </div>
  );
}

export function ErrorState({ message }: { message: string }) {
  return (
    <div className="rounded-md border border-red-400/30 bg-red-500/10 p-6 text-red-100">
      {message}
    </div>
  );
}

export function EmptyState({ message }: { message: string }) {
  return (
    <div className="rounded-md border border-white/10 bg-[#111111] p-6 text-zinc-400">
      {message}
    </div>
  );
}
