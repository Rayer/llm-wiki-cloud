"use client";

import { useCallback, useEffect, useState } from "react";
import {
  compareConceptConflict,
  getConceptConflictOperation,
  getConceptConflicts,
  resolveConceptConflict,
  type ConceptConflict,
  type ConceptConflictComparison,
  type ConceptConflictInventory,
  type ConceptConflictResolveAction,
} from "@/lib/api";
import { useT } from "@/lib/i18n";
import { useWorkspace } from "@/components/WorkspaceProvider";

function wait(milliseconds: number) {
  return new Promise((resolve) => window.setTimeout(resolve, milliseconds));
}

export default function ConceptConflictsPage() {
  const { t } = useT();
  const { currentProject, hydrated } = useWorkspace();
  const projectID = currentProject?.id;
  const [inventory, setInventory] = useState<ConceptConflictInventory | null>(null);
  const [selectedPath, setSelectedPath] = useState("");
  const [comparison, setComparison] = useState<ConceptConflictComparison | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");

  const reload = useCallback(async () => {
    if (!projectID) {
      setInventory(null);
      setComparison(null);
      setSelectedPath("");
      return;
    }
    setLoading(true);
    setError("");
    try {
      const result = await getConceptConflicts(projectID);
      setInventory(result);
      setComparison(null);
      setSelectedPath("");
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setLoading(false);
    }
  }, [projectID]);

  useEffect(() => {
    if (!hydrated) return;
    let cancelled = false;
    const clearProject = () => {
      if (cancelled) return;
      setInventory(null);
      setComparison(null);
      setSelectedPath("");
      setLoading(false);
    };
    if (!projectID) {
      void Promise.resolve().then(clearProject);
      return () => { cancelled = true; };
    }
    void getConceptConflicts(projectID)
      .then((result) => {
        if (cancelled) return;
        setInventory(result);
        setComparison(null);
        setSelectedPath("");
      })
      .catch((cause) => {
        if (!cancelled) setError(cause instanceof Error ? cause.message : String(cause));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => { cancelled = true; };
  }, [hydrated, projectID]);

  const selectConflict = async (conflict: ConceptConflict) => {
    setSelectedPath(conflict.published.path);
    setComparison(null);
    setError("");
    setMessage("");
    try {
      setComparison(await compareConceptConflict(conflict.published.path, projectID));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    }
  };

  const decide = async (action: ConceptConflictResolveAction) => {
    if (!comparison || !projectID || busy || !comparison.conflict_id) return;
    const confirmation = action === "keep_published"
      ? t("ConceptConflicts.confirmKeep")
      : t("ConceptConflicts.confirmPublish");
    if (!window.confirm(confirmation)) return;
    setBusy(true);
    setError("");
    setMessage(t("ConceptConflicts.submitting"));
    try {
      const operationID = crypto.randomUUID();
      const accepted = await resolveConceptConflict({
        operation_id: operationID,
        action,
        conflict_id: comparison.conflict_id,
        path: comparison.published.path,
        storage_view: comparison.storage_view,
        pair_sha256: comparison.pair_sha256,
      }, projectID);
      if (accepted.status !== "pending") {
        if (accepted.status === "committed") {
          setMessage(action === "keep_published" ? t("ConceptConflicts.kept") : t("ConceptConflicts.published"));
        } else {
          setMessage(t("ConceptConflicts.operationFinished", { status: accepted.status }));
        }
        await reload();
        return;
      }
      if (!accepted.execution_id) throw new Error(t("ConceptConflicts.missingExecution"));

      let finalStatus = "pending";
      for (let attempt = 0; attempt < 90; attempt += 1) {
        await wait(2000);
        const result = await getConceptConflictOperation(operationID, accepted.execution_id, projectID);
        finalStatus = result.status;
        if (finalStatus !== "pending") break;
      }
      if (finalStatus === "committed") {
        setMessage(action === "keep_published" ? t("ConceptConflicts.kept") : t("ConceptConflicts.published"));
      } else if (finalStatus === "pending") {
        setMessage(t("ConceptConflicts.stillRunning"));
      } else {
        setMessage(t("ConceptConflicts.operationFinished", { status: finalStatus }));
      }
      await reload();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
      setMessage("");
      await reload();
    } finally {
      setBusy(false);
    }
  };

  const conflicts = inventory?.conflicts ?? [];
  const heldBack = inventory?.held_back ?? [];

  return (
    <main className="mx-auto w-full max-w-7xl space-y-6 px-4 py-6 sm:px-6">
      <header className="space-y-2">
        <h1 className="text-3xl font-semibold tracking-tight text-white">{t("ConceptConflicts.title")}</h1>
        <p className="max-w-3xl text-sm text-zinc-400">{t("ConceptConflicts.description")}</p>
      </header>

      {error ? <p role="alert" className="rounded border border-red-500/40 bg-red-950/40 p-3 text-sm text-red-200">{error}</p> : null}
      {message ? <p role="status" className="rounded border border-white/15 bg-white/5 p-3 text-sm text-zinc-200">{message}</p> : null}
      {loading ? <p role="status" className="text-sm text-zinc-400">{t("ConceptConflicts.loading")}</p> : null}
      {!loading && inventory ? (
        <p className="text-sm text-zinc-300">
          {t("ConceptConflicts.counts", { newCount: inventory.new_count, pendingCount: inventory.pending_count })}
        </p>
      ) : null}

      {!loading && conflicts.length === 0 && heldBack.length === 0 ? (
        <p className="rounded border border-white/10 p-4 text-zinc-400">{t("ConceptConflicts.empty")}</p>
      ) : null}

      {conflicts.length > 0 ? (
        <section className="grid gap-6 lg:grid-cols-[minmax(18rem,0.8fr)_minmax(0,1.2fr)]" aria-label={t("ConceptConflicts.title")}>
          <div className="space-y-2">
            <h2 className="text-lg font-medium text-white">{t("ConceptConflicts.pending")}</h2>
            <ul className="space-y-2">
              {conflicts.map((conflict) => (
                <li key={conflict.conflict_id}>
                  <button
                    type="button"
                    onClick={() => void selectConflict(conflict)}
                    disabled={busy}
                    aria-current={selectedPath === conflict.published.path ? "true" : undefined}
                    className="w-full rounded border border-white/10 p-3 text-left text-sm text-zinc-200 hover:bg-white/5 disabled:opacity-50"
                  >
                    <span className="block break-all font-medium">{conflict.published.path}</span>
                    <span className="mt-1 block text-xs text-zinc-400">
                      {conflict.is_new ? t("ConceptConflicts.newlyDetected") : t("ConceptConflicts.pendingItem")} · {t("ConceptConflicts.confidence", { value: conflict.confidence })}
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          </div>

          <div className="min-w-0 space-y-4">
            {comparison ? (
              <>
                <div className="space-y-1">
                  <h2 className="break-all text-lg font-medium text-white">{comparison.published.path}</h2>
                  <p className="break-all text-xs text-zinc-500">{t("ConceptConflicts.snapshot", { value: comparison.storage_view })}</p>
                  <p className="text-xs text-zinc-400">{t("ConceptConflicts.threshold", { confidence: comparison.confidence, minimum: comparison.min_confidence })}</p>
                </div>
                <div className="grid gap-4 xl:grid-cols-2">
                  <ConflictVersion title={t("ConceptConflicts.publishedVersion")} version={comparison.published} />
                  <ConflictVersion title={t("ConceptConflicts.candidateVersion")} version={comparison.candidate} />
                </div>
                <div className="flex flex-wrap gap-3">
                  <button type="button" disabled={busy || comparison.confidence < comparison.min_confidence} onClick={() => void decide("keep_published")} className="min-h-11 rounded border border-white/20 px-4 text-sm text-zinc-100 hover:bg-white/10 disabled:opacity-50">
                    {t("ConceptConflicts.keepAction")}
                  </button>
                  <button type="button" disabled={busy || comparison.confidence < comparison.min_confidence} onClick={() => void decide("publish_candidate")} className="min-h-11 rounded bg-emerald-700 px-4 text-sm text-white hover:bg-emerald-600 disabled:opacity-50">
                    {t("ConceptConflicts.publishAction")}
                  </button>
                </div>
                {comparison.confidence < comparison.min_confidence ? <p className="text-sm text-amber-200">{t("ConceptConflicts.belowThreshold")}</p> : null}
              </>
            ) : (
              <p className="rounded border border-white/10 p-4 text-sm text-zinc-400">{t("ConceptConflicts.selectHint")}</p>
            )}
          </div>
        </section>
      ) : null}

      {heldBack.length > 0 ? (
        <section className="space-y-2">
          <h2 className="text-lg font-medium text-white">{t("ConceptConflicts.heldBack")}</h2>
          <p className="text-sm text-zinc-400">{t("ConceptConflicts.heldBackDescription")}</p>
          <ul className="space-y-1">
            {heldBack.map((conflict) => <li key={conflict.conflict_id} className="break-all rounded border border-amber-500/20 p-3 text-sm text-amber-100">
              {conflict.published.path} · {t("ConceptConflicts.threshold", { confidence: conflict.confidence, minimum: conflict.min_confidence })}
            </li>)}
          </ul>
        </section>
      ) : null}
    </main>
  );
}

function ConflictVersion({ title, version }: { title: string; version: ConceptConflictComparison["published"] }) {
  const { t } = useT();
  return (
    <section className="min-w-0 rounded border border-white/10 bg-white/[0.02]">
      <div className="space-y-1 border-b border-white/10 p-3">
        <h3 className="text-sm font-medium text-white">{title}</h3>
        <p className="break-all font-mono text-[11px] text-zinc-500">{version.path}</p>
        <p className="break-all font-mono text-[11px] text-zinc-500">SHA-256 {version.sha256}</p>
        <p className="text-xs text-zinc-400">{t("ConceptConflicts.rowState", { value: version.identity.exists ? version.identity.status ?? t("ConceptConflicts.rowPresent") : t("ConceptConflicts.rowMissing") })}</p>
        {version.identity.exists ? <p className="break-all text-[11px] text-zinc-500">
          {[version.identity.content_hash && `content ${version.identity.content_hash}`, version.identity.article_id && `article ${version.identity.article_id}`, version.identity.entity_id && `entity ${version.identity.entity_id}`].filter(Boolean).join(" · ")}
        </p> : null}
      </div>
      <pre className="max-h-[32rem] overflow-auto whitespace-pre-wrap break-words p-3 text-xs leading-5 text-zinc-200">{version.content ?? ""}</pre>
    </section>
  );
}
