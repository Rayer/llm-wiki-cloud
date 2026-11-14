// Package profilelimits centralizes the input and guidance budgets shared by
// the Profile API, derivation pipeline, artifact writers, and Worker.
package profilelimits

import (
	"fmt"
	"unicode/utf8"
)

const (
	MaxRequirementRows         = 20
	MaxRequirementTextScalars  = 2_000
	MaxProfileTextScalars      = 10_000
	MaxGuidanceScalars         = 6_000
	MaxProfileRequestBodyBytes = 1 << 20
)

const (
	ViolationRequirementCount = "requirement_count"
	ViolationRequirementText  = "requirement_text"
	ViolationProfileTextTotal = "profile_text_total"
	ViolationCompileGuidance  = "compile_guidance"
)

// Violation contains only a stable category and numeric bounds. It never
// stores or formats the text being validated.
type Violation struct {
	Kind   string
	Max    int
	Actual int
}

func (v *Violation) Error() string {
	if v == nil {
		return "Profile limit exceeded"
	}
	return fmt.Sprintf("Profile %s limit exceeded (%d > %d)", v.Kind, v.Actual, v.Max)
}

// CountScalars counts Unicode scalar values in the exact UTF-8 string. It does
// not normalize, trim, or count grapheme clusters.
func CountScalars(value string) int { return utf8.RuneCountInString(value) }

// ValidateRequirementTexts enforces the Profile row and text budgets in a
// deterministic order: row count, first oversized row, then aggregate text.
func ValidateRequirementTexts(texts []string) *Violation {
	if len(texts) > MaxRequirementRows {
		return &Violation{Kind: ViolationRequirementCount, Max: MaxRequirementRows, Actual: len(texts)}
	}
	total := 0
	for _, text := range texts {
		count := CountScalars(text)
		if count > MaxRequirementTextScalars {
			return &Violation{Kind: ViolationRequirementText, Max: MaxRequirementTextScalars, Actual: count}
		}
		total += count
	}
	if total > MaxProfileTextScalars {
		return &Violation{Kind: ViolationProfileTextTotal, Max: MaxProfileTextScalars, Actual: total}
	}
	return nil
}

func ValidateGuidance(value string) *Violation {
	count := CountScalars(value)
	if count > MaxGuidanceScalars {
		return &Violation{Kind: ViolationCompileGuidance, Max: MaxGuidanceScalars, Actual: count}
	}
	return nil
}
