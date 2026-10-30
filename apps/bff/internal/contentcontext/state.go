package contentcontext

import (
	"strings"
	"time"
)

type Status string

const (
	StatusMissing Status = "missing"
	StatusPending Status = "pending"
	StatusReady   Status = "ready"
	StatusFailed  Status = "failed"

	maxClaims = 2
)

// Publication identifies a confirmed project publication. Callers must verify
// the current manifest and publisher fence before passing it to this model.
type Publication struct {
	Generation       string
	ConceptsDigest   string
	ManifestIdentity string
	TargetSequence   uint64
}

type Summary struct {
	ConceptsDigest   string
	SourceGeneration string
	Text             string
}

type Attempt struct {
	ConceptsDigest         string
	ID                     string
	SourceGeneration       string
	SourceManifestIdentity string
	LeaseUntil             time.Time
}

type Work struct {
	ConceptsDigest         string
	SourceGeneration       string
	SourceManifestIdentity string
	Status                 Status
	ClaimCount             int
	RetryCount             int
	Attempt                Attempt
	HasAttempt             bool
}

// State keeps the latest confirmed publication separate from digest-keyed
// summary work. It is a value model: transitions return a new snapshot.
type State struct {
	LatestPublication Publication
	HasPublication    bool
	Work              Work
	HasWork           bool
	LastGood          Summary
	HasLastGood       bool
}

// ConfirmPublication advances to the publication that the caller has verified
// as current while holding the publisher fence. TargetSequence records
// registration order and never chooses which manifest is current.
func (s State) ConfirmPublication(p Publication) (State, bool) {
	if strings.TrimSpace(p.Generation) == "" || strings.TrimSpace(p.ConceptsDigest) == "" ||
		strings.TrimSpace(p.ManifestIdentity) == "" || p.TargetSequence == 0 {
		return s, false
	}
	if s.HasPublication {
		current := s.LatestPublication
		if p.ManifestIdentity == current.ManifestIdentity {
			if p.Generation != current.Generation || p.ConceptsDigest != current.ConceptsDigest {
				return s, false
			}
			if p.TargetSequence > current.TargetSequence {
				s.LatestPublication.TargetSequence = p.TargetSequence
			}
			return s, true
		}
	}

	previousDigest := ""
	if s.HasPublication {
		previousDigest = s.LatestPublication.ConceptsDigest
	}
	s.LatestPublication = p
	s.HasPublication = true
	if p.ConceptsDigest == previousDigest && s.HasWork && s.Work.ConceptsDigest == p.ConceptsDigest {
		return s, true
	}

	if s.HasLastGood && s.LastGood.ConceptsDigest == p.ConceptsDigest {
		s.Work = Work{
			ConceptsDigest: p.ConceptsDigest, SourceGeneration: s.LastGood.SourceGeneration,
			Status: StatusReady,
		}
	} else {
		s.Work = Work{
			ConceptsDigest: p.ConceptsDigest, SourceGeneration: p.Generation,
			SourceManifestIdentity: p.ManifestIdentity, Status: StatusPending,
		}
	}
	s.HasWork = true
	return s, true
}

// Status reports the work state for the latest confirmed publication.
func (s State) Status() Status {
	if !s.HasPublication || !s.HasWork || s.Work.ConceptsDigest != s.LatestPublication.ConceptsDigest {
		return StatusMissing
	}
	return s.Work.Status
}

// Stale reports whether the retained last-good summary describes another
// concepts digest. The summary remains available while stale.
func (s State) Stale() bool {
	return s.HasLastGood && (!s.HasPublication || s.LastGood.ConceptsDigest != s.LatestPublication.ConceptsDigest)
}

// ClaimAttempt creates a bounded claim or takes over an expired claim. A
// same-digest publication carry-forward preserves the existing attempt.
func (s State) ClaimAttempt(id string, now time.Time, lease time.Duration) (State, Attempt, bool) {
	if strings.TrimSpace(id) == "" || lease <= 0 || !s.HasPublication || !s.HasWork ||
		s.Work.Status != StatusPending || s.Work.ConceptsDigest != s.LatestPublication.ConceptsDigest {
		return s, Attempt{}, false
	}
	if s.Work.HasAttempt && now.Before(s.Work.Attempt.LeaseUntil) {
		return s, Attempt{}, false
	}
	if s.Work.ClaimCount >= maxClaims {
		s.Work.Status = StatusFailed
		s.Work.Attempt = Attempt{}
		s.Work.HasAttempt = false
		return s, Attempt{}, false
	}

	s.Work.ClaimCount++
	if s.Work.ClaimCount > 1 {
		s.Work.RetryCount++
	}
	attempt := Attempt{
		ConceptsDigest:         s.Work.ConceptsDigest,
		ID:                     id,
		SourceGeneration:       s.Work.SourceGeneration,
		SourceManifestIdentity: s.Work.SourceManifestIdentity,
		LeaseUntil:             now.Add(lease),
	}
	s.Work.Attempt = attempt
	s.Work.HasAttempt = true
	return s, attempt, true
}

// CompleteAttempt accepts only the current, unexpired attempt for the current
// digest. It deliberately does not require the target generation or sequence
// to match, so same-digest carry-forward can retain the original source.
func (s State) CompleteAttempt(attempt Attempt, now time.Time, summary string) (State, bool) {
	if !s.validAttempt(attempt, now) {
		return s, false
	}
	s.LastGood = Summary{
		ConceptsDigest:   attempt.ConceptsDigest,
		SourceGeneration: attempt.SourceGeneration,
		Text:             summary,
	}
	s.HasLastGood = true
	s.Work.Status = StatusReady
	s.Work.Attempt = Attempt{}
	s.Work.HasAttempt = false
	return s, true
}

// FailAttempt exhausts the current work. Same-digest carry-forward will keep
// it failed; only a different digest or explicit maintenance action may reopen.
func (s State) FailAttempt(attempt Attempt, now time.Time) (State, bool) {
	if !s.validAttempt(attempt, now) {
		return s, false
	}
	s.Work.Status = StatusFailed
	s.Work.Attempt = Attempt{}
	s.Work.HasAttempt = false
	return s, true
}

func (s State) validAttempt(attempt Attempt, now time.Time) bool {
	return s.HasPublication && s.HasWork && s.Work.Status == StatusPending &&
		s.Work.ConceptsDigest == s.LatestPublication.ConceptsDigest &&
		s.Work.HasAttempt && s.Work.Attempt == attempt && now.Before(attempt.LeaseUntil)
}
