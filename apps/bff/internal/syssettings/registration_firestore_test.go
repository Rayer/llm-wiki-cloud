package syssettings

import (
	"context"
	"net"
	"os"
	"testing"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/option"
)

func TestRegistrationMethodsPersistAcrossStoreInstances(t *testing.T) {
	endpoint := os.Getenv("FIRESTORE_EMULATOR_HOST")
	if endpoint == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}
	host, _, err := net.SplitHostPort(endpoint)
	if err != nil || (host != "localhost" && !net.ParseIP(host).IsLoopback()) {
		t.Fatal("registration persistence test requires a loopback emulator")
	}
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "lwc-324-settings-test", option.WithEndpoint(endpoint), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	open := true
	store := NewStore(client, &open)
	for _, email := range []bool{false, true} {
		for _, google := range []bool{false, true} {
			if _, err := store.settingsRef().Set(ctx, map[string]interface{}{"registration_enabled": false, "announcement_published_markdown": "retain me"}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.SetRegistrationSettings(ctx, nil, &email, nil); err != nil {
				t.Fatal(err)
			}
			partial, err := NewStore(client, &open).GetSettings(ctx)
			if err != nil || partial.RegistrationEnabled || !partial.GoogleRegistrationEnabled {
				t.Fatalf("partial migration changed master or missing preference: %+v error=%v", partial, err)
			}
			if _, err := store.SetRegistrationSettings(ctx, nil, nil, &google); err != nil {
				t.Fatal(err)
			}
			got, err := NewStore(client, &open).GetSettings(ctx)
			if err != nil || got.EmailRegistrationEnabled != email || got.GoogleRegistrationEnabled != google || got.AnnouncementMarkdown != "retain me" {
				t.Fatalf("reload=%+v err=%v", got, err)
			}
			for _, master := range []bool{true, false, true} {
				if _, err := store.SetRegistrationSettings(ctx, &master, nil, nil); err != nil {
					t.Fatal(err)
				}
				reloaded := NewStore(client, &open)
				got, err := reloaded.GetSettings(ctx)
				if err != nil || got.RegistrationEnabled != master || got.EmailRegistrationEnabled != email || got.GoogleRegistrationEnabled != google {
					t.Fatalf("master toggle lost preferences: %+v err=%v", got, err)
				}
				for method, want := range map[string]bool{"email": master && email, "google": master && google} {
					got, err := reloaded.IsRegistrationEnabled(ctx, method)
					if err != nil || got != want {
						t.Fatalf("%s gate=%t want=%t err=%v", method, got, want, err)
					}
				}
				doc, err := store.settingsRef().Get(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if doc.Data()["registration_enabled"] != master || doc.Data()["email_registration_enabled"] != email || doc.Data()["google_registration_enabled"] != google {
					t.Fatal("persisted master/preferences changed")
				}
			}
		}
	}
}
