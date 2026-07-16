type BadgeVariant = 'default' | 'source' | 'concept' | 'published' | 'draft' | 'accent' | 'muted';

const variantStyles: Record<BadgeVariant, string> = {
  default: 'bg-white/10 text-zinc-300 ring-white/10',
  source: 'bg-blue-500/15 text-blue-300 ring-blue-400/20',
  concept: 'bg-violet-500/15 text-violet-300 ring-violet-400/20',
  published: 'bg-emerald-500/15 text-emerald-300 ring-emerald-400/20',
  draft: 'bg-amber-500/15 text-amber-300 ring-amber-400/20',
  accent: 'bg-emerald-500/15 text-emerald-300 ring-emerald-400/20',
  muted: 'bg-zinc-800 text-zinc-400 ring-white/5',
};

export function Badge({
  children,
  variant = 'default',
  className = '',
}: {
  children: React.ReactNode;
  variant?: BadgeVariant;
  className?: string;
}) {
  return (
    <span
      className={`inline-flex items-center gap-1 rounded-md px-2 py-0.5 text-xs font-medium ring-1 ring-inset ${variantStyles[variant]} ${className}`}
    >
      {children}
    </span>
  );
}