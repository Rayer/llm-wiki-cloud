package auth_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/rayer/gateauth/internal/auth"
	"github.com/rayer/gateauth/internal/syssettings"
	"google.golang.org/api/option"
)

type registrationStoreAdapter struct{ store *syssettings.Store }

func (a registrationStoreAdapter) GetRegistrationSettings(ctx context.Context) (auth.ManagementRegistrationSettings, error) {
	settings, err := a.store.GetRegistrationSettings(ctx)
	return auth.ManagementRegistrationSettings{RegistrationEnabled: settings.RegistrationEnabled,
		EmailRegistrationEnabled: settings.EmailRegistrationEnabled, GoogleRegistrationEnabled: settings.GoogleRegistrationEnabled}, err
}

func (a registrationStoreAdapter) SetRegistrationSettingsInTransaction(ctx context.Context, tx *firestore.Transaction,
	master, email, google *bool) (auth.ManagementRegistrationSettings, error) {
	settings, err := a.store.SetRegistrationSettingsInTransaction(ctx, tx, master, email, google)
	return auth.ManagementRegistrationSettings{RegistrationEnabled: settings.RegistrationEnabled,
		EmailRegistrationEnabled: settings.EmailRegistrationEnabled, GoogleRegistrationEnabled: settings.GoogleRegistrationEnabled}, err
}

func TestRegistrationSettingsWriteRechecksCurrentAuthAdmin(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if endpoint == "" {
		t.Skip("owned loopback Firestore emulator required")
	}
	if !strings.HasPrefix(endpoint, "127.0.0.1:") && !strings.HasPrefix(endpoint, "localhost:") {
		t.Fatal("registration authority test requires loopback Firestore emulator")
	}
	ctx := context.Background()
	project := fmt.Sprintf("auth-registration-%d", time.Now().UnixNano())
	fs, err := firestore.NewClient(ctx, project, option.WithEndpoint("http://"+endpoint), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	environment := "registration-test-" + project
	actorID, otherID := "actor-"+project, "other-"+project
	for _, id := range []string{actorID, otherID} {
		if _, err := fs.Collection("users").Doc(id).Set(ctx, auth.UserRecord{Role: "admin", Status: auth.AccountActive, AuthVersion: 1}); err != nil {
			t.Fatal(err)
		}
	}
	authority := auth.NewManagementAuthority(fs, environment)
	epoch, err := authority.InitializeLegacyShadow(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	store := registrationStoreAdapter{store: syssettings.NewStore(fs, nil)}
	if _, err := fs.Collection("system").Doc("settings").Set(ctx, map[string]any{
		"registration_enabled": true, "email_registration_enabled": true, "google_registration_enabled": true,
	}); err != nil {
		t.Fatal(err)
	}
	caps := map[string]int64{auth.ManagementScopeAuthAdmin: epoch.Epoch}
	closed := false
	if _, err := authority.UpdateRegistrationSettings(ctx, actorID, caps, 1, store, &closed, nil, nil); err != nil {
		t.Fatalf("current auth:admin was rejected: %v", err)
	}
	member := "member"
	if err := authority.UpdateLegacyAccount(ctx, otherID, map[string]int64{auth.ManagementScopeAuthAdmin: epoch.Epoch}, 1,
		actorID, &member, nil); err != nil {
		t.Fatalf("remove actor's legacy auth:admin authority: %v", err)
	}
	open := true
	if _, err := authority.UpdateRegistrationSettings(ctx, actorID, caps, 1, store, &open, nil, nil); err == nil {
		t.Fatal("revoked actor auth:admin changed registration policy")
	}
	settings, err := store.GetRegistrationSettings(ctx)
	if err != nil || settings.RegistrationEnabled {
		t.Fatalf("revoked actor changed persisted policy: settings=%+v err=%v", settings, err)
	}
}
