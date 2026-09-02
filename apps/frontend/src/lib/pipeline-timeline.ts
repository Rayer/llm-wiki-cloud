import type { PipelineExecution } from './api';

export const PIPELINE_STEPS = ['ingest', 'compile', 'lint', 'publish'] as const;

const PIPELINE_STAGE_LABELS: Record<string, string> = {
  input_materialization: 'Input materialization',
  synto_migration: 'Synto migration',
  synto_config_normalization: 'Synto config normalization',
  synto_config_validation: 'Synto config validation',
  synto_run: 'Synto run',
  synto_index_export: 'Synto index export',
  source_reconciliation: 'Source reconciliation',
  concept_reconciliation: 'Concept reconciliation',
  postprocess: 'Postprocess',
  generation_publish: 'Generation publish',
  receipt_recording: 'Receipt recording',
  lease_cleanup: 'Lease cleanup',
};

export function getPipelineTimelineState(execution?: PipelineExecution | null) {
  const status = execution?.status;
  const diagnosticStage = execution?.diagnostic?.stage ?? null;
  return {
    completedSteps: status === 'SUCCEEDED'
      ? PIPELINE_STEPS.length
      : status === 'RUNNING'
        ? 1
        : 0,
    failedStep: null,
    stageLabel: diagnosticStage ? PIPELINE_STAGE_LABELS[diagnosticStage] ?? 'stage unavailable' : 'stage unavailable',
  };
}
