package llm

import (
	"context"
	"errors"
	"sync"
)

type ProfileEligibilityCheck func(context.Context, ProfileConfig) error

type ProfileOperationState struct {
	mu     sync.Mutex
	worked bool
}

func (s *ProfileOperationState) hasWorked() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.worked
}

func (s *ProfileOperationState) markWorked() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.worked = true
	s.mu.Unlock()
}

// ProfilePoolClient performs a bounded, ordered pool traversal. It advances
// only after an explicit quota-exhaustion response; other provider failures
// stop the operation. The pool is created per operation, never shared.
type ProfilePoolClient struct {
	profiles   []*ProfileClient
	check      ProfileEligibilityCheck
	state      *ProfileOperationState
	mu         sync.Mutex
	exhausted  map[string]struct{}
	last       ProfileConfig
	lastModel  string
	lastResult CompletionMetadata
}

func NewProfilePoolClient(profiles []*ProfileClient, check ProfileEligibilityCheck) (*ProfilePoolClient, error) {
	return NewProfilePoolClientWithState(profiles, check, &ProfileOperationState{})
}

func NewProfilePoolClientWithState(profiles []*ProfileClient, check ProfileEligibilityCheck, state *ProfileOperationState) (*ProfilePoolClient, error) {
	if len(profiles) == 0 {
		return nil, errors.New("shared LLM pool is unavailable")
	}
	copyOfProfiles := append([]*ProfileClient(nil), profiles...)
	for _, profile := range copyOfProfiles {
		if profile == nil {
			return nil, errors.New("shared LLM pool contains an invalid profile")
		}
	}
	if state == nil {
		state = &ProfileOperationState{}
	}
	return &ProfilePoolClient{profiles: copyOfProfiles, check: check, state: state, exhausted: make(map[string]struct{})}, nil
}

func (c *ProfilePoolClient) Complete(ctx context.Context, systemPrompt, userMessage string) (CompletionResult, error) {
	if c == nil {
		return CompletionResult{}, &ProfileCallError{Kind: ProviderFailure}
	}
	allowFallback := !c.state.hasWorked()
	for _, profile := range c.profiles {
		configuration := profile.ProfileConfig()
		c.mu.Lock()
		_, exhausted := c.exhausted[configuration.ID]
		c.mu.Unlock()
		if exhausted {
			continue
		}
		if c.check != nil {
			if err := c.check(ctx, configuration); err != nil {
				return CompletionResult{}, err
			}
		}
		result, err := profile.Complete(ctx, systemPrompt, userMessage)
		if err != nil {
			if IsQuotaExhausted(err) {
				if !allowFallback {
					return CompletionResult{}, err
				}
				c.mu.Lock()
				c.exhausted[configuration.ID] = struct{}{}
				c.mu.Unlock()
				continue
			}
			return CompletionResult{}, err
		}
		c.mu.Lock()
		c.last, c.lastModel, c.lastResult = configuration, result.Metadata.ReturnedModel, result.Metadata
		c.mu.Unlock()
		c.state.markWorked()
		return result, nil
	}
	return CompletionResult{}, &ProfileCallError{Kind: ProviderQuotaExhausted, StatusCode: 429}
}

func (c *ProfilePoolClient) Chat(ctx context.Context, systemPrompt, userMessage string) (string, error) {
	result, err := c.Complete(ctx, systemPrompt, userMessage)
	return result.Text, err
}

func (c *ProfilePoolClient) ChatWithMetadata(ctx context.Context, systemPrompt, userMessage string) (string, string, error) {
	result, err := c.Complete(ctx, systemPrompt, userMessage)
	return result.Text, result.Metadata.ReturnedModel, err
}

func (c *ProfilePoolClient) ModelIdentity() (ModelIdentity, bool) {
	if c == nil || len(c.profiles) == 0 {
		return ModelIdentity{}, false
	}
	c.mu.Lock()
	selected := c.last
	c.mu.Unlock()
	if selected.ID == "" {
		selected = c.profiles[0].ProfileConfig()
	}
	return ModelIdentity{Provider: selected.Provider, Model: selected.Model}, true
}

func (c *ProfilePoolClient) ProfileIdentity() ProfileIdentity {
	if c == nil || len(c.profiles) == 0 {
		return ProfileIdentity{}
	}
	c.mu.Lock()
	selected := c.last
	c.mu.Unlock()
	if selected.ID == "" {
		selected = c.profiles[0].ProfileConfig()
	}
	return ProfileIdentity{ID: selected.ID, Revision: selected.Revision, Role: string(selected.Role), Provider: selected.Provider, Protocol: string(selected.Protocol), Model: selected.Model}
}

func (c *ProfilePoolClient) LastMetadata() (CompletionMetadata, bool) {
	if c == nil {
		return CompletionMetadata{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastResult, c.lastResult.ProfileID != ""
}

func (c *ProfilePoolClient) Close() {
	if c == nil {
		return
	}
	for _, profile := range c.profiles {
		profile.Close()
	}
}
