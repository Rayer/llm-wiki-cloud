package queryquality

import (
	"context"
	"errors"
)

// StageReplay is an opt-in local experiment boundary. The normal runtime has no
// replay context. Callers must bind saved values to the exact corpus, request,
// implementation and configuration before installing this context.
// Each invocation executes exactly one stage; earlier stages consume saved values.
type StageReplay struct {
	Stage     int                `json:"stage"`
	Plan      *QueryPlan         `json:"plan,omitempty"`
	Expansion ExpansionInfo      `json:"expansion"`
	Matching  *EligibilityResult `json:"matching,omitempty"`
	Selection *SelectionResult   `json:"selection,omitempty"`
}
type stageReplayKey struct{}

func WithStageReplay(ctx context.Context, replay *StageReplay) (context.Context, error) {
	if replay == nil || (replay.Stage != 70 && replay.Stage != 80 && replay.Stage != 90 && replay.Stage != 100) {
		return nil, errors.New("invalid query replay stage")
	}
	if replay.Stage > 70 && replay.Plan == nil {
		return nil, errors.New("stage requires saved expansion")
	}
	if replay.Stage > 80 && replay.Matching == nil {
		return nil, errors.New("stage requires saved matching")
	}
	if replay.Stage > 90 && replay.Selection == nil {
		return nil, errors.New("stage requires saved selection")
	}
	// A newly executed stage invalidates outputs below it even when a caller
	// reuses the same serialized state object.
	if replay.Stage == 70 {
		replay.Matching, replay.Selection = nil, nil
	}
	if replay.Stage == 80 {
		replay.Selection = nil
	}
	return context.WithValue(ctx, stageReplayKey{}, replay), nil
}
func stageReplay(ctx context.Context) *StageReplay {
	r, _ := ctx.Value(stageReplayKey{}).(*StageReplay)
	return r
}
