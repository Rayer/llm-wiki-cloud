package v1

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	"github.com/rayer/llm-wiki-bff/internal/authconsumer"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type defaultProjectBootstrapRequest struct {
	SchemaVersion int `json:"schema_version"`
}

type defaultProjectBootstrapResponse struct {
	SchemaVersion    int    `json:"schema_version"`
	Status           string `json:"status"`
	DefaultProjectID string `json:"default_project_id"`
}

var errDefaultProjectSetupConflict = errors.New("default Project setup conflicts with stored identity")

func (h *Handler) BootstrapDefaultProject(c *gin.Context) {
	var request defaultProjectBootstrapRequest
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || request.SchemaVersion != 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	credential := requestUserCredential(c)
	rawIdentity, exists := c.Get("authConsumerIdentity")
	identity, ok := rawIdentity.(authconsumer.Identity)
	consumer, hasOnboarding := h.consumerVerifier.(onboardingConsumer)
	if !exists || !ok || !hasOnboarding || strings.TrimSpace(credential) == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "onboarding identity authority unavailable"})
		return
	}
	onboarding, err := h.currentOnboardingIdentity(c.Request.Context(), consumer, credential, identity)
	if err != nil {
		statusCode := http.StatusServiceUnavailable
		message := "onboarding identity authority unavailable"
		switch {
		case errors.Is(err, authconsumer.ErrInvalidCredential), errors.Is(err, authconsumer.ErrInactiveAccount):
			statusCode, message = http.StatusUnauthorized, "invalid token"
		case errors.Is(err, authconsumer.ErrConflict):
			statusCode, message = http.StatusConflict, "setup_conflict"
		}
		c.JSON(statusCode, gin.H{"error": message})
		return
	}
	if !auth.ValidPathSegment(onboarding.DefaultProjectID) {
		c.JSON(http.StatusConflict, gin.H{"error": "setup_conflict"})
		return
	}
	if h.firestore == nil || h.firestore.Raw() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Project setup unavailable"})
		return
	}
	if err := ensureDefaultProject(c.Request.Context(), h.firestore.Raw(), onboarding.Subject, onboarding.DefaultProjectID); err != nil {
		if errors.Is(err, errDefaultProjectSetupConflict) {
			c.JSON(http.StatusConflict, gin.H{"error": "setup_conflict"})
			return
		}
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Project setup pending"})
		return
	}
	c.JSON(http.StatusOK, defaultProjectBootstrapResponse{
		SchemaVersion: 1, Status: "ready", DefaultProjectID: onboarding.DefaultProjectID,
	})
}

func (h *Handler) currentOnboardingIdentity(ctx context.Context, consumer onboardingConsumer, credential string,
	verified authconsumer.Identity) (authconsumer.OnboardingIdentity, error) {
	identity, err := consumer.OnboardingIdentity(ctx, credential)
	if err == nil && identity.Subject == verified.Subject && identity.AuthVersion == verified.AuthVersion {
		return identity, nil
	}
	if err != nil {
		return authconsumer.OnboardingIdentity{}, err
	}
	refreshed, err := h.consumerVerifier.Verify(ctx, credential, "")
	if err != nil {
		return authconsumer.OnboardingIdentity{}, err
	}
	identity, err = consumer.OnboardingIdentity(ctx, credential)
	if err != nil || identity.Subject != refreshed.Subject || identity.AuthVersion != refreshed.AuthVersion {
		return authconsumer.OnboardingIdentity{}, authconsumer.ErrUnavailable
	}
	return identity, nil
}

func ensureDefaultProject(ctx context.Context, fs *firestore.Client, owner, projectID string) error {
	if fs == nil || !auth.ValidPathSegment(owner) || !auth.ValidPathSegment(projectID) {
		return errDefaultProjectSetupConflict
	}
	projects := scopedfirestore.Collection(fs, "projects")
	projectDocs := projects
	metadataRef := scopedfirestore.Collection(fs, "users").Doc(owner).Collection("projects").Doc(projectID)
	return fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		documents, err := tx.Documents(projectDocs).GetAll()
		if err != nil {
			return err
		}
		metadataSnapshot, err := tx.Get(metadataRef)
		metadataExists := err == nil
		if err != nil && status.Code(err) != codes.NotFound {
			return err
		}
		var metadata map[string]interface{}
		if metadataExists {
			metadata = metadataSnapshot.Data()
			if value, exists := metadata["user_id"]; exists && value != owner {
				return errDefaultProjectSetupConflict
			}
			if value, exists := metadata["project_id"]; exists && value != projectID {
				return errDefaultProjectSetupConflict
			}
			if _, exists := metadata["name"]; exists {
				if _, ok := metadata["name"].(string); !ok {
					return errDefaultProjectSetupConflict
				}
			}
			if _, exists := metadata["created_at"]; exists && !validProjectCreatedAt(metadata["created_at"]) {
				return errDefaultProjectSetupConflict
			}
		}

		var match *firestore.DocumentSnapshot
		for _, document := range documents {
			data := document.Data()
			kind, record, valid := classifyAdminProjectDoc(document.Ref.ID, data)
			if valid {
				if kind == adminIdempotencyMarkerDoc {
					continue
				}
				if record.userID == owner && record.projectID == projectID {
					if !validDefaultProjectRecord(data) {
						return errDefaultProjectSetupConflict
					}
					if match != nil {
						return errDefaultProjectSetupConflict
					}
					match = document
				}
				continue
			}
			if !strings.HasPrefix(document.Ref.ID, owner+"_") {
				continue
			}
			storedOwner, ownerPresent := data["user_id"]
			if ownerPresent && storedOwner != owner {
				continue
			}
			storedProject, projectPresent := data["project_id"]
			if projectPresent {
				if storedProject != projectID {
					continue
				}
				parsedProject, parsedOwner, parsed := projectResponseFromFirestoreDoc(document.Ref.ID, data)
				if !parsed || parsedOwner != owner || parsedProject.ID != projectID || !validDefaultProjectRecord(data) {
					return errDefaultProjectSetupConflict
				}
				if match != nil {
					return errDefaultProjectSetupConflict
				}
				match = document
				continue
			}
			if !canRecoverProjectFromOwnerSuffix(document.Ref.ID, owner, projectID) || !validDefaultProjectRecord(data) {
				return errDefaultProjectSetupConflict
			}
			if match != nil {
				return errDefaultProjectSetupConflict
			}
			match = document
		}

		name := "My First Wiki"
		if match != nil {
			if value, ok := match.Data()["name"].(string); ok && strings.TrimSpace(value) != "" {
				name = value
			}
		}
		if metadataExists {
			if value, ok := metadata["name"].(string); ok && strings.TrimSpace(value) != "" {
				name = value
			}
		}
		now := time.Now().UTC()
		createdAt := interface{}(now)
		if metadataExists {
			if value, exists := metadata["created_at"]; exists {
				createdAt = value
			}
		}
		if match != nil {
			if value, exists := match.Data()["created_at"]; exists {
				createdAt = value
			}
		}

		if match == nil {
			projectRef := projects.Doc(projectDocID(owner, projectID))
			if err := tx.Create(projectRef, map[string]interface{}{
				"user_id": owner, "project_id": projectID, "name": name, "status": "ready", "created_at": createdAt,
			}); err != nil {
				return err
			}
		} else {
			projectData := match.Data()
			updates := map[string]interface{}{}
			if _, exists := projectData["user_id"]; !exists {
				updates["user_id"] = owner
			}
			if _, exists := projectData["project_id"]; !exists {
				updates["project_id"] = projectID
			}
			if value, ok := projectData["name"].(string); !ok || strings.TrimSpace(value) == "" {
				updates["name"] = name
			}
			if value, exists := projectData["status"]; !exists || value == "" {
				updates["status"] = "ready"
			} else if _, ok := value.(string); !ok {
				return errDefaultProjectSetupConflict
			}
			if _, exists := projectData["created_at"]; !exists {
				updates["created_at"] = createdAt
			}
			if len(updates) > 0 {
				if err := tx.Set(match.Ref, updates, firestore.MergeAll); err != nil {
					return err
				}
			}
		}
		if !metadataExists {
			if err := tx.Create(metadataRef, map[string]interface{}{"name": name, "created_at": createdAt}); err != nil {
				return err
			}
		} else {
			metadataUpdates := map[string]interface{}{}
			if value, exists := metadata["name"]; !exists || strings.TrimSpace(value.(string)) == "" {
				metadataUpdates["name"] = name
			}
			if _, exists := metadata["created_at"]; !exists {
				metadataUpdates["created_at"] = createdAt
			}
			if len(metadataUpdates) > 0 {
				if err := tx.Set(metadataRef, metadataUpdates, firestore.MergeAll); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func canRecoverProjectFromOwnerSuffix(docID, owner, projectID string) bool {
	return auth.ValidPathSegment(owner) && auth.ValidPathSegment(projectID) && docID == projectDocID(owner, projectID)
}

func validDefaultProjectRecord(data map[string]interface{}) bool {
	if value, exists := data["name"]; exists {
		if _, ok := value.(string); !ok {
			return false
		}
	}
	if value, exists := data["status"]; exists {
		if _, ok := value.(string); !ok {
			return false
		}
	}
	if value, exists := data["created_at"]; exists && !validProjectCreatedAt(value) {
		return false
	}
	return true
}

func validProjectCreatedAt(value interface{}) bool {
	if value == nil {
		return true
	}
	_, ok := value.(time.Time)
	return ok
}
