package syssettings

import (
	"context"
	"fmt"
	"sync"
	"unicode/utf8"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	settingsCollection = "system"
	settingsDocID      = "settings"
	maxMarkdownBytes   = 32 << 10
)

// Settings is the public system settings payload.
type Settings struct {
	RegistrationEnabled       bool   `json:"registration_enabled"` // Legacy: both methods are open.
	EmailRegistrationEnabled  bool   `json:"email_registration_enabled"`
	GoogleRegistrationEnabled bool   `json:"google_registration_enabled"`
	AnnouncementMarkdown      string `json:"announcement_markdown"`
}

// PublicSettings is the deliberately metadata-free anonymous response.
type PublicSettings struct {
	RegistrationEnabled       bool   `json:"registration_enabled"` // Legacy: both methods are open.
	EmailRegistrationEnabled  bool   `json:"email_registration_enabled"`
	GoogleRegistrationEnabled bool   `json:"google_registration_enabled"`
	AnnouncementMarkdown      string `json:"announcement_markdown"`
	// AnnouncementDigest is nil when announcement_markdown is empty; otherwise it is lowercase sha256:<64 lowercase hex>.
	AnnouncementDigest *string `json:"announcement_digest" extensions:"x-nullable" example:"sha256:315f5bdb76d078c43b8ac0064e4a0164612b1fce77c869345bfc94c75894edd3"`
}

// RegistrationGate reports whether self-serve registration is allowed.
type RegistrationGate interface {
	IsRegistrationEnabled(ctx context.Context, method string) (bool, error)
	GetSettings(ctx context.Context) (Settings, error)
	SetRegistrationEnabled(ctx context.Context, enabled bool) (Settings, error)
	SetRegistrationMethods(ctx context.Context, email, google *bool) (Settings, error)
	PublishAnnouncement(ctx context.Context, markdown string) (Settings, error)
}

// Store resolves and persists registration settings in Firestore with env fallback.
type Store struct {
	fs       *firestore.Client
	envValue *bool
}

// NewStore creates a settings store. envValue is nil when REGISTRATION_ENABLED is unset.
func NewStore(fs *firestore.Client, envValue *bool) *Store {
	return &Store{fs: fs, envValue: envValue}
}

func (s *Store) settingsRef() *firestore.DocumentRef {
	return s.fs.Collection(settingsCollection).Doc(settingsDocID)
}

// IsRegistrationEnabled checks only the requested new-account method.
func (s *Store) IsRegistrationEnabled(ctx context.Context, method string) (bool, error) {
	settings, err := s.GetSettings(ctx)
	if err != nil {
		return false, err
	}
	switch method {
	case "email":
		return settings.EmailRegistrationEnabled, nil
	case "google":
		return settings.GoogleRegistrationEnabled, nil
	default:
		return false, fmt.Errorf("unknown registration method")
	}
}

// resolveSettings migrates missing method fields from the legacy posture.
// Present but invalid values close that method; they never fall back to open.
func resolveSettings(data map[string]interface{}, envValue *bool) Settings {
	legacy, _ := data["registration_enabled"].(bool)
	fallback := Resolve(data != nil, legacy, envValue)
	method := func(key string) bool {
		value, exists := data[key]
		if !exists {
			return fallback
		}
		enabled, _ := value.(bool)
		return enabled
	}
	email, google := method("email_registration_enabled"), method("google_registration_enabled")
	markdown, _ := data["announcement_published_markdown"].(string)
	return Settings{RegistrationEnabled: email && google, EmailRegistrationEnabled: email, GoogleRegistrationEnabled: google, AnnouncementMarkdown: markdown}
}

func (s *Store) GetSettings(ctx context.Context) (Settings, error) {
	if s.fs == nil {
		return resolveSettings(nil, s.envValue), nil
	}
	doc, err := s.settingsRef().Get(ctx)
	if status.Code(err) == codes.NotFound {
		return resolveSettings(nil, s.envValue), nil
	}
	if err != nil {
		return Settings{}, fmt.Errorf("get system settings: %w", err)
	}
	return resolveSettings(doc.Data(), s.envValue), nil
}

func validateMarkdown(markdown string) error {
	if !utf8.ValidString(markdown) {
		return fmt.Errorf("announcement markdown is not valid UTF-8")
	}
	if len([]byte(markdown)) > maxMarkdownBytes {
		return fmt.Errorf("announcement markdown exceeds %d bytes", maxMarkdownBytes)
	}
	return nil
}

func (s *Store) PublishAnnouncement(ctx context.Context, markdown string) (Settings, error) {
	if err := validateMarkdown(markdown); err != nil {
		return Settings{}, err
	}
	if s.fs == nil {
		return Settings{}, fmt.Errorf("Firestore client is not configured")
	}
	err := s.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		return tx.Set(s.settingsRef(), map[string]interface{}{"announcement_published_markdown": markdown}, firestore.MergeAll)
	})
	if err != nil {
		return Settings{}, fmt.Errorf("publish announcement: %w", err)
	}
	return s.GetSettings(ctx)
}

// SetRegistrationEnabled is the legacy API: explicitly set both methods.
func (s *Store) SetRegistrationEnabled(ctx context.Context, enabled bool) (Settings, error) {
	return s.SetRegistrationMethods(ctx, &enabled, &enabled)
}

// SetRegistrationMethods resolves and writes both methods atomically so a partial
// update cannot reopen the other method during legacy migration.
func (s *Store) SetRegistrationMethods(ctx context.Context, email, google *bool) (Settings, error) {
	if s.fs == nil {
		return Settings{}, fmt.Errorf("Firestore client is not configured")
	}
	var settings Settings
	err := s.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		doc, err := tx.Get(s.settingsRef())
		var data map[string]interface{}
		if err == nil {
			data = doc.Data()
		} else if status.Code(err) != codes.NotFound {
			return err
		}
		settings = resolveSettings(data, s.envValue)
		if email != nil {
			settings.EmailRegistrationEnabled = *email
		}
		if google != nil {
			settings.GoogleRegistrationEnabled = *google
		}
		settings.RegistrationEnabled = settings.EmailRegistrationEnabled && settings.GoogleRegistrationEnabled
		return tx.Set(s.settingsRef(), map[string]interface{}{
			"email_registration_enabled":  settings.EmailRegistrationEnabled,
			"google_registration_enabled": settings.GoogleRegistrationEnabled,
			"registration_enabled":        settings.RegistrationEnabled,
		}, firestore.MergeAll)
	})
	if err != nil {
		return Settings{}, fmt.Errorf("set system settings: %w", err)
	}
	return settings, nil
}

// FakeStore is an in-memory RegistrationGate for tests.
type FakeStore struct {
	Enabled       bool
	EmailEnabled  *bool
	GoogleEnabled *bool
	Err           error
	Persisted     *bool
	GetCalls      int
	SetCalls      int
	LastSetValue  bool
	Published     string
	PublishErr    error
	mu            sync.RWMutex
}

func (f *FakeStore) IsRegistrationEnabled(ctx context.Context, method string) (bool, error) {
	settings, err := f.GetSettings(ctx)
	if err != nil {
		return false, err
	}
	switch method {
	case "email":
		return settings.EmailRegistrationEnabled, nil
	case "google":
		return settings.GoogleRegistrationEnabled, nil
	default:
		return false, fmt.Errorf("unknown registration method")
	}
}

func (f *FakeStore) settings() Settings {
	email, google := f.Enabled, f.Enabled
	if f.EmailEnabled != nil {
		email = *f.EmailEnabled
	}
	if f.GoogleEnabled != nil {
		google = *f.GoogleEnabled
	}
	return Settings{RegistrationEnabled: email && google, EmailRegistrationEnabled: email, GoogleRegistrationEnabled: google, AnnouncementMarkdown: f.Published}
}

func (f *FakeStore) GetSettings(ctx context.Context) (Settings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.GetCalls++
	if f.Err != nil {
		return Settings{}, f.Err
	}
	return f.settings(), nil
}

func (f *FakeStore) PublishAnnouncement(ctx context.Context, markdown string) (Settings, error) {
	if err := validateMarkdown(markdown); err != nil {
		return Settings{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.PublishErr != nil {
		return Settings{}, f.PublishErr
	}
	f.Published = markdown
	return f.settings(), nil
}

func (f *FakeStore) SetRegistrationEnabled(ctx context.Context, enabled bool) (Settings, error) {
	return f.SetRegistrationMethods(ctx, &enabled, &enabled)
}

func (f *FakeStore) SetRegistrationMethods(ctx context.Context, email, google *bool) (Settings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.SetCalls++
	if f.Err != nil {
		return Settings{}, f.Err
	}
	if email != nil {
		value := *email
		f.EmailEnabled = &value
	}
	if google != nil {
		value := *google
		f.GoogleEnabled = &value
	}
	settings := f.settings()
	emailValue, googleValue := settings.EmailRegistrationEnabled, settings.GoogleRegistrationEnabled
	f.EmailEnabled, f.GoogleEnabled = &emailValue, &googleValue
	f.Enabled = settings.RegistrationEnabled
	f.LastSetValue = f.Enabled
	value := f.Enabled
	f.Persisted = &value
	return settings, nil
}
