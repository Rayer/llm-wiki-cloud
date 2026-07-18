export type NavigationDecision = 'allow' | 'confirm';

export function navigationDecision({
  blocked,
  button = 0,
  modified = false,
  target,
  download = false,
}: {
  blocked: boolean;
  button?: number;
  modified?: boolean;
  target?: string | null;
  download?: boolean;
}): NavigationDecision {
  if (!blocked || button !== 0 || modified || download || target === '_blank') return 'allow';
  return 'confirm';
}
