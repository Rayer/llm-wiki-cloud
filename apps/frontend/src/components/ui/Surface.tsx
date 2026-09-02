import type { HTMLAttributes, ReactNode } from 'react';

type SurfaceProps = HTMLAttributes<HTMLElement> & {
  children: ReactNode;
  className?: string;
  variant?: 'default' | 'glass' | 'elevated';
  as?: 'div' | 'section' | 'article';
};

const variants = {
  default: 'border border-white/10 bg-zinc-900/50',
  glass: 'glass-card',
  elevated: 'border border-white/10 bg-zinc-900/80 shadow-lg shadow-black/20',
};

export function Surface({
  children,
  className = '',
  variant = 'default',
  as: Tag = 'div',
  ...props
}: SurfaceProps) {
  return (
    <Tag className={`rounded-[var(--radius-lg)] ${variants[variant]} ${className}`} {...props}>
      {children}
    </Tag>
  );
}