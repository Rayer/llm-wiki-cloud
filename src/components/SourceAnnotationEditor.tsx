'use client';

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { ApiError, getSourceAnnotation, updateSourceAnnotation, type SourceAnnotation } from '@/lib/api';
import { useT } from '@/lib/i18n';
import { annotationClearDecision, annotationDirtyMetadata, annotationErrorKey, annotationLifecycleKey, annotationLoadDecision, annotationSaveDecision, createAnnotationRequestGate, isAnnotationDirty } from '@/lib/source-annotation';
import { useWorkspace } from './WorkspaceProvider';
import { useNavigationBlocker } from './NavigationBlocker';
import { Surface } from './ui/Surface';

type EditorState = SourceAnnotation & { draft: string };

export function SourceAnnotationEditor({ sourceId, onSaved }: { sourceId: string; onSaved?: () => void }) {
  const { t } = useT();
  const { isDemoSession, refreshNavCounts } = useWorkspace();
  const { setBlocked } = useNavigationBlocker();
  const [state, setState] = useState<EditorState | null>(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [disabledReason, setDisabledReason] = useState<'readOnly' | 'unavailable' | 'locked' | null>(null);
  const [error, setError] = useState<'loadFailed' | 'saveFailed' | null>(null);
  const [conflict, setConflict] = useState(false);
  const requestGate = useRef(createAnnotationRequestGate());

  const load = useCallback(async () => {
    const request = requestGate.current.begin();
    setState(null);
    setLoading(true);
    setSaving(false);
    setError(null);
    setConflict(false);
    setDisabledReason(null);
    if (annotationLoadDecision(isDemoSession) === 'skip') {
      if (requestGate.current.isCurrent(request)) setLoading(false);
      return;
    }
    try {
      const annotation = await getSourceAnnotation(sourceId);
      if (!requestGate.current.isCurrent(request)) return;
      setState({ ...annotation, draft: annotation.body });
    } catch (cause) {
      if (!requestGate.current.isCurrent(request)) return;
      const reason = cause instanceof ApiError ? annotationErrorKey(cause.status) : null;
      if (reason) {
        setDisabledReason(reason);
      } else {
        setError('loadFailed');
      }
    } finally {
      if (requestGate.current.isCurrent(request)) setLoading(false);
    }
  }, [isDemoSession, sourceId]);

  useEffect(() => {
    const gate = requestGate.current;
    let active = true;
    void Promise.resolve().then(() => {
      if (active) return load();
    });
    return () => {
      active = false;
      gate.invalidate();
    };
  }, [load]);

  const dirty = useMemo(
    () => Boolean(state && isAnnotationDirty(state.body, state.draft)),
    [state],
  );
  useEffect(() => {
    setBlocked(dirty);
    return () => setBlocked(false);
  }, [dirty, setBlocked]);

  const save = async (body = state?.draft ?? '') => {
    if (!state || annotationSaveDecision({ body: state.body, draft: body, saving }) === 'noop') return;
    const request = requestGate.current.begin();
    setSaving(true);
    setError(null);
    setConflict(false);
    try {
      const annotation = await updateSourceAnnotation(sourceId, body, state.expectedGeneration);
      if (!requestGate.current.isCurrent(request)) return;
      setState({ ...annotation, draft: annotation.body });
      void refreshNavCounts();
      window.dispatchEvent(new Event('source-annotation-saved'));
      onSaved?.();
    } catch (cause) {
      if (!requestGate.current.isCurrent(request)) return;
      if (cause instanceof ApiError && cause.status === 412) {
        setConflict(true);
      } else if (cause instanceof ApiError && annotationErrorKey(cause.status)) {
        setDisabledReason(annotationErrorKey(cause.status));
      } else {
        setError('saveFailed');
      }
    } finally {
      if (requestGate.current.isCurrent(request)) setSaving(false);
    }
  };

  const clear = () => {
    if (!state || annotationClearDecision(state.draft) === 'noop') return;
    if (window.confirm(t('Annotation.clearConfirm'))) void save('');
  };

  return (
    <Surface variant="glass" className="p-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h2 className="text-lg font-semibold text-white">{t('Annotation.title')}</h2>
        <span aria-live="polite" className="text-sm text-zinc-400">{dirty ? t('Annotation.dirty') : t('Annotation.saved')}</span>
      </div>
      {loading ? <p className="mt-3 text-sm text-zinc-400">{t('Annotation.loading')}</p> : null}
      {isDemoSession ? <p className="mt-2 text-sm text-zinc-400">{t('Annotation.demoDisabled')}</p> : null}
      {disabledReason ? <p role="status" className="mt-3 text-sm text-zinc-400">{t(`Annotation.${disabledReason}`)}</p> : null}
      {error ? (
        <div role="alert" className="mt-3 flex flex-wrap items-center gap-3 text-sm text-red-200">
          <span>{t(`Annotation.${error}`)}</span>
          <button type="button" onClick={() => void load()} disabled={loading || saving} className="underline disabled:opacity-50">
            {t('Annotation.retry')}
          </button>
        </div>
      ) : null}
      {conflict ? <div role="alert" className="mt-3 flex flex-wrap items-center gap-3 text-sm text-amber-100"><span>{t('Annotation.conflict')}</span><button type="button" onClick={() => void load()} className="underline">{t('Annotation.reload')}</button></div> : null}
      {state && !disabledReason && !isDemoSession ? (
        <>
          <label htmlFor="source-annotation" className="mt-4 block text-sm font-medium text-zinc-200">{t('Annotation.label')}</label>
          <textarea
            id="source-annotation"
            value={state.draft}
            disabled={saving}
            onChange={(event) => setState((current) => current ? { ...current, draft: event.target.value } : current)}
            className="mt-2 min-h-40 w-full rounded-md border border-white/10 bg-black/20 p-3 text-sm text-white outline-none focus:border-emerald-400/50 focus:ring-1 focus:ring-emerald-400/20 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-400 disabled:opacity-60"
          />
          <AnnotationMetadata annotation={state} />
          <div className="mt-4 flex flex-wrap gap-3">
            <button type="button" disabled={!dirty || saving} onClick={() => void save()} className="min-h-11 rounded-md bg-emerald-400 px-4 text-sm font-medium text-zinc-950 disabled:cursor-not-allowed disabled:opacity-50">{saving ? t('Annotation.saving') : t('Annotation.save')}</button>
            <button type="button" disabled={saving || annotationClearDecision(state.draft) === 'noop'} onClick={clear} className="min-h-11 rounded-md border border-white/10 px-4 text-sm text-zinc-200 disabled:cursor-not-allowed disabled:opacity-50">{t('Annotation.clear')}</button>
          </div>
        </>
      ) : null}
    </Surface>
  );
}

function AnnotationMetadata({ annotation }: { annotation: SourceAnnotation }) {
  const { t } = useT();
  const lifecycleKey = annotationLifecycleKey(annotation.lifecycleStatus);
  const lifecycle = lifecycleKey
    ? t(lifecycleKey)
    : annotation.lifecycleStatus ? t('Annotation.lifecycleUnknown') : undefined;
  const dirty = annotationDirtyMetadata(annotation).map((key) => t(`Annotation.${key}`));

  if (!annotation.updatedAt && !lifecycle && dirty.length === 0) return null;
  return (
    <dl className="mt-3 grid gap-x-4 gap-y-1 text-xs text-zinc-500 sm:grid-cols-2">
      {lifecycle ? <><dt>{t('Annotation.lifecycle')}</dt><dd>{lifecycle}</dd></> : null}
      {dirty.length > 0 ? <><dt>{t('Annotation.dirtyMetadata')}</dt><dd>{dirty.join(' · ')}</dd></> : null}
      {annotation.updatedAt ? <><dt>{t('Annotation.updatedAt')}</dt><dd>{annotation.updatedAt}</dd></> : null}
    </dl>
  );
}
