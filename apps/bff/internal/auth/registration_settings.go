package auth

import (
	"context"

	"cloud.google.com/go/firestore"
)

// ManagementRegistrationSettings is the Auth-owned registration policy
// exposed to consumers and administrator routes.
type ManagementRegistrationSettings struct {
	RegistrationEnabled       bool `json:"registration_enabled"`
	EmailRegistrationEnabled  bool `json:"email_registration_enabled"`
	GoogleRegistrationEnabled bool `json:"google_registration_enabled"`
}

// RegistrationSettingsAuthority adapts the backing policy store without
// making Auth depend on the BFF settings package.
type RegistrationSettingsAuthority interface {
	GetRegistrationSettings(context.Context) (ManagementRegistrationSettings, error)
	SetRegistrationSettingsInTransaction(context.Context, *firestore.Transaction, *bool, *bool, *bool) (ManagementRegistrationSettings, error)
}
