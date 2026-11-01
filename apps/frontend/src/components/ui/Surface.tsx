import type { HTMLAttributes, ReactNode } from 'react';

type SurfaceProps = HTMLAttributes<HTMLElement> & {
  children: ReactNode;
  className?: string;
  variant?: 'default' | 'glass' | 'elevated';
  as?: 'div' | 'section' | 'article';
};

const variants = {
  default: 'lwc-surface',
  glass: 'lwc-surface',
  elevated: 'lwc-surface bg-zinc-800',
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