package auth

import (
	"context"
	"errors"
	"math"

	"cloud.google.com/go/firestore"
)

const (
	AccountActive    = "active"
	AccountSuspended = "suspended"
)

var (
	ErrAccountInactive       = errors.New("account is not active")
	ErrAccountUnavailable    = errors.New("account access unavailable")
	ErrAccountAdminRequired  = errors.New("active admin required")
	ErrAccountSelfSuspension = errors.New("cannot suspend yourself")
	ErrAccountLastAdmin      = errors.New("at least one active admin is required")
	ErrAccountUpdateInvalid  = errors.New("invalid account update")
)

type AccountLookup func(context.Context, string) (*UserRecord, error)

func FirestoreAccountLookup(fs *firestore.Client) AccountLookup {
	return func(ctx context.Context, id string) (*UserRecord, error) {
		if fs == nil || !ValidPathSegment(id) {
			return nil, ErrAccountUnavailable
		}
		return GetUser(ctx, fs, id)
	}
}

func (u *UserRecord) Active() bool {
	return u != nil && (u.Status == "" || u.Status == AccountActive) && u.AuthVersion >= 0
}

func (u *UserRecord) AllowsVersion(version int64) bool {
	return u.Active() && version == u.AuthVersion
}

// UpdateAccount serializes lifecycle/role changes with the admin documents it
// counts and with session transactions reading the target account. Existing
// documents without status/auth_version are active at version zero.
func UpdateAccount(ctx context.Context, fs *firestore.Client, actorID, targetID string, role, state *string) error {
	if fs == nil || !ValidPathSegment(actorID) || !ValidPathSegment(targetID) || (role == nil && state == nil) {
		return ErrAccountUpdateInvalid
	}
	if role != nil && *role == "" {
		return ErrAccountUpdateInvalid
	}
	if state != nil && *state != AccountActive && *state != AccountSuspended {
		return ErrAccountUpdateInvalid
	}
	if actorID == targetID && state != nil && *state == AccountSuspended {
		return ErrAccountSelfSuspension
	}
	return fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		actorDoc, err := tx.Get(fs.Collection("users").Doc(actorID))
		if err != nil {
			return ErrAccountAdminRequired
		}
		var actor UserRecord
		if err := actorDoc.DataTo(&actor); err != nil || !actor.Active() || actor.Role != "admin" {
			return ErrAccountAdminRequired
		}
		targetRef := fs.Collection("users").Doc(targetID)
		targetDoc, err := tx.Get(targetRef)
		if err != nil {
			return err
		}
		var target UserRecord
		if err := targetDoc.DataTo(&target); err != nil {
			return err
		}
		next := target
		if role != nil {
			next.Role = *role
		}
		if state != nil {
			next.Status = *state
		}
		if target.Active() && target.Role == "admin" && (!next.Active() || next.Role != "admin") {
			// ponytail: read all admin documents for atomic last-admin protection;
			// replace with an explicit membership authority only if admin count grows large.
			admins, err := tx.Documents(fs.Collection("users").Where("role", "==", "admin")).GetAll()
			if err != nil {
				return err
			}
			active := 0
			for _, doc := range admins {
				var admin UserRecord
				if err := doc.DataTo(&admin); err != nil {
					return err
				}
				if admin.Active() {
					active++
				}
			}
			if active <= 1 {
				return ErrAccountLastAdmin
			}
		}
		updates := make([]firestore.Update, 0, 3)
		if role != nil {
			updates = append(updates, firestore.Update{Path: "role", Value: *role})
		}
		if state != nil {
			updates = append(updates, firestore.Update{Path: "status", Value: *state})
			if *state == AccountSuspended && target.Status != AccountSuspended {
				if target.AuthVersion < 0 || target.AuthVersion == math.MaxInt64 {
					return ErrAccountUpdateInvalid
				}
				updates = append(updates, firestore.Update{Path: "auth_version", Value: target.AuthVersion + 1}, firestore.Update{Path: "auth_invalid_before", Value: firestore.ServerTimestamp})
			}
			if *state == AccountActive && target.Status == AccountSuspended {
				// ServerTimestamp is coarse request time. Before becoming active,
				// retain the suspended snapshot's precise commit boundary so an
				// OAuth start racing suspension cannot revive after restoration.
				updates = append(updates, firestore.Update{Path: "auth_invalid_before", Value: targetDoc.UpdateTime})
			}
		}
		return tx.Update(targetRef, updates)
	})
}
