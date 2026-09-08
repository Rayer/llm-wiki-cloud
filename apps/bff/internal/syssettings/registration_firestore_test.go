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
			if _, err := store.SetRegistrationMethods(ctx, &email, nil); err != nil {
				t.Fatal(err)
			}
			partial, err := NewStore(client, &open).GetSettings(ctx)
			if err != nil || partial.GoogleRegistrationEnabled {
				t.Fatalf("partial migration reopened Google: %+v error=%v", partial, err)
			}
			if _, err := store.SetRegistrationMethods(ctx, nil, &google); err != nil {
				t.Fatal(err)
			}
			got, err := NewStore(client, &open).GetSettings(ctx)
			if err != nil || got.EmailRegistrationEnabled != email || got.GoogleRegistrationEnabled != google || got.AnnouncementMarkdown != "retain me" {
				t.Fatalf("reload=%+v err=%v", got, err)
			}
			doc, err := store.settingsRef().Get(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if doc.Data()["registration_enabled"] != (email && google) {
				t.Fatal("legacy persisted flag is not conservative")
			}
		}
	}
}
