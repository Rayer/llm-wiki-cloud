export const PROFILE_LIMITS = {
  requirementRows: 20,
  requirementTextScalars: 2_000,
  profileTextScalars: 10_000,
  guidanceScalars: 6_000,
  requestBodyBytes: 1_048_576,
} as const;

export type ProfileLimitViolation = {
  kind: 'requirement_count' | 'requirement_text' | 'profile_text_total';
  max: number;
  actual: number;
  index?: number;
};

export function countUnicodeScalars(value: string): number {
  return Array.from(value).length;
}

export function validateProfileRequirements(
  requirements: ReadonlyArray<{ text: string }>,
): ProfileLimitViolation | null {
  if (requirements.length > PROFILE_LIMITS.requirementRows) {
    return {
      kind: 'requirement_count',
      max: PROFILE_LIMITS.requirementRows,
      actual: requirements.length,
    };
  }
  let total = 0;
  for (const [index, requirement] of requirements.entries()) {
    const actual = countUnicodeScalars(requirement.text);
    if (actual > PROFILE_LIMITS.requirementTextScalars) {
      return {
        kind: 'requirement_text',
        max: PROFILE_LIMITS.requirementTextScalars,
        actual,
        index,
      };
    }
    total += actual;
  }
  if (total > PROFILE_LIMITS.profileTextScalars) {
    return {
      kind: 'profile_text_total',
      max: PROFILE_LIMITS.profileTextScalars,
      actual: total,
    };
  }
  return null;
}
