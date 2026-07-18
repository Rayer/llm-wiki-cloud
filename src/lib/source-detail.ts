/** Incrementing this key makes the detail loader refetch after an annotation save. */
export function nextSourceDetailReloadVersion(version: number): number {
  return version + 1;
}
