package urlintake

import "math"

type QualityStatus string

const (
	QualityUsable            QualityStatus = "usable"
	QualityNeedsConfirmation QualityStatus = "needs_confirmation"
	QualityRejected          QualityStatus = "rejected"
)

// QualityThresholds are intake-specific and must be selected against labeled
// URL cases. Jev probabilities are internal decision inputs, not user-facing
// confidence values.
type QualityThresholds struct {
	RejectShellAt       float64
	RejectContentAtMost float64
	UsableShellAtMost   float64
	UsableContentAt     float64
}

func DecideQuality(evaluation QualityEvaluation, thresholds QualityThresholds) QualityStatus {
	if evaluation.ShellProbability >= thresholds.RejectShellAt && evaluation.ContentProbability <= thresholds.RejectContentAtMost {
		return QualityRejected
	}
	if evaluation.ShellProbability <= thresholds.UsableShellAtMost && evaluation.ContentProbability >= thresholds.UsableContentAt {
		return QualityUsable
	}
	return QualityNeedsConfirmation
}

func validQualityEvaluation(evaluation QualityEvaluation) bool {
	for _, probability := range []float64{evaluation.ShellProbability, evaluation.ContentProbability} {
		if math.IsNaN(probability) || math.IsInf(probability, 0) || probability < 0 || probability > 1 {
			return false
		}
	}
	return true
}
