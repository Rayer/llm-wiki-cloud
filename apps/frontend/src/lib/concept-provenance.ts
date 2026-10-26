export type ConceptProvenance = {
  compileRun: string | null;
  implementation: string | null;
  version: string | null;
  revision: string | null;
};

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function nonEmptyString(value: unknown): string | null {
  return typeof value === 'string' && value.trim() ? value.trim() : null;
}

export function latestConceptProvenance(frontmatter: Record<string, unknown> | undefined): ConceptProvenance {
  const lineage = frontmatter?.lineage;
  const latestCompileRun = Array.isArray(lineage)
    ? lineage.filter((entry) => (
      isRecord(entry)
      && (Object.prototype.hasOwnProperty.call(entry, 'compile_run') || entry.operation === 'compile_run')
    )).at(-1)
    : undefined;
  const generator = isRecord(latestCompileRun?.generator) ? latestCompileRun.generator : {};

  return {
    compileRun: latestCompileRun ? nonEmptyString(latestCompileRun.compile_run) : null,
    implementation: nonEmptyString(generator.implementation),
    version: nonEmptyString(generator.version),
    revision: nonEmptyString(generator.revision),
  };
}
