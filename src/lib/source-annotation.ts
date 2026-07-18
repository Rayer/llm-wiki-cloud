export type AnnotationSaveDecision = 'save' | 'noop';
export type AnnotationLoadDecision = 'load' | 'skip';

export type AnnotationRequestGate = {
  begin: () => number;
  isCurrent: (request: number) => boolean;
  invalidate: () => void;
};

export function createAnnotationRequestGate(): AnnotationRequestGate {
  let current = 0;

  return {
    begin: () => ++current,
    isCurrent: (request) => request === current,
    invalidate: () => { ++current; },
  };
}

export function annotationLoadDecision(isDemoSession: boolean): AnnotationLoadDecision {
  return isDemoSession ? 'skip' : 'load';
}

export function normalizeAnnotationBody(body: string): string {
  return body.replace(/\r\n?/g, '\n').trim();
}

export function normalizeAnnotationGeneration(value: unknown): string | null {
  if (typeof value !== 'string' && typeof value !== 'number') return null;
  const generation = String(value).trim();
  return generation ? generation : null;
}

export function isAnnotationDirty(body: string, draft: string): boolean {
  return normalizeAnnotationBody(body) !== normalizeAnnotationBody(draft);
}

export function annotationSaveDecision({
  body,
  draft,
  saving,
}: {
  body: string;
  draft: string;
  saving: boolean;
}): AnnotationSaveDecision {
  return saving || !isAnnotationDirty(body, draft) ? 'noop' : 'save';
}

export function annotationClearDecision(draft: string): AnnotationSaveDecision {
  return normalizeAnnotationBody(draft) ? 'save' : 'noop';
}

export function annotationErrorKey(status: number): 'readOnly' | 'unavailable' | 'locked' | null {
  if (status === 403) return 'readOnly';
  if (status === 404) return 'unavailable';
  if (status === 409) return 'locked';
  return null;
}

export function annotationLifecycleKey(status?: string): string | null {
  return status && ['new', 'synced', 'notes_pending', 'content_pending', 'error'].includes(status)
    ? `Source.lifecycle.${status}`
    : null;
}

export function annotationDirtyMetadata({
  dirty,
  annotationDirty,
  rawDirty,
}: {
  dirty: boolean;
  annotationDirty: boolean;
  rawDirty: boolean;
}): Array<'dirtyOverall' | 'dirtyAnnotation' | 'dirtyRaw'> {
  return [
    ...(dirty ? ['dirtyOverall' as const] : []),
    ...(annotationDirty ? ['dirtyAnnotation' as const] : []),
    ...(rawDirty ? ['dirtyRaw' as const] : []),
  ];
}
