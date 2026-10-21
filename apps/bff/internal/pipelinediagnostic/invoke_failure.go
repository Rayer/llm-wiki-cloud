package pipelinediagnostic

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

const ConfirmedInvokeFailureMessage = "pipeline invocation failed before an execution was accepted"

// ConfirmedInvokeFailure is the shared marker written when invocation was
// definitively rejected before an execution was accepted. AttemptID is only
// present for scheduled reservations, whose stable daily reservation ID may
// be reused after a confirmed-not-invoked release.
type ConfirmedInvokeFailure struct {
	Version    int        `json:"version"`
	Status     string     `json:"status"`
	AttemptID  string     `json:"attempt_id,omitempty"`
	Stage      Stage      `json:"stage"`
	ErrorClass ErrorClass `json:"error_class"`
	Execution  string     `json:"execution"`
	Message    string     `json:"message"`
}

// DecodeConfirmedInvokeFailure keeps the recovery marker schema strict while
// accepting both legacy/manual markers without attempt_id and current
// scheduled markers with an attempt incarnation.
func DecodeConfirmedInvokeFailure(data []byte) (ConfirmedInvokeFailure, error) {
	var evidence ConfirmedInvokeFailure
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&evidence); err != nil {
		return ConfirmedInvokeFailure{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return ConfirmedInvokeFailure{}, errors.New("trailing JSON value")
		}
		return ConfirmedInvokeFailure{}, err
	}
	return evidence, nil
}

// Confirms reports whether this strict marker is valid for the exact
// reservation and, when required, its scheduled attempt incarnation.
func (e ConfirmedInvokeFailure) Confirms(reservationID, attemptID string, requireAttempt bool) bool {
	_, validStage := ValidStages[e.Stage]
	_, validClass := ValidErrorClasses[e.ErrorClass]
	if e.Version != 1 || e.Status != "failed" || e.Execution != reservationID ||
		e.Message != ConfirmedInvokeFailureMessage || !validStage || !validClass {
		return false
	}
	if requireAttempt {
		return attemptID != "" && e.AttemptID == attemptID
	}
	return e.AttemptID == ""
}
