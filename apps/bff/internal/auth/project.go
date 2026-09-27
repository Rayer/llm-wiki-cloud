package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	ErrProjectPermissionDenied      = errors.New("project permission denied")
	ErrProjectPermissionUnavailable = errors.New("project permission unavailable")
	ErrProjectPageTokenInvalid      = errors.New("invalid project page token")
)

// ProjectOwnerAuthorizer checks the current owner authority for one project.
type ProjectOwnerAuthorizer func(context.Context, string, string) error

// ProjectSummary is the stable CLI project-list view.
type ProjectSummary struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

// FirestoreProjectOwnerAuthorizer returns the shared owner check used by Auth
// and BFF CLI middleware. The project document and current user are the
// authority; client-supplied project IDs alone never grant access.
func FirestoreProjectOwnerAuthorizer(fs *firestore.Client) ProjectOwnerAuthorizer {
	return func(ctx context.Context, userID, projectID string) error {
		userID, projectID = strings.TrimSpace(userID), strings.TrimSpace(projectID)
		if fs == nil {
			return ErrProjectPermissionUnavailable
		}
		if !ValidPathSegment(userID) || !ValidPathSegment(projectID) {
			return ErrProjectPermissionDenied
		}
		ref := fs.Collection("projects").Doc(projectDocumentID(userID, projectID))
		snapshot, err := ref.Get(ctx)
		if err == nil {
			if _, ok := projectFromFirestoreDocument(userID, snapshot.Ref.ID, snapshot.Data()); ok {
				return nil
			}
			return ErrProjectPermissionDenied
		}
		if status.Code(err) != codes.NotFound {
			return ErrProjectPermissionUnavailable
		}

		// Older real project records can use a display label as the document
		// suffix while storing their stable project_id in the document.
		iter := fs.Collection("projects").Documents(ctx)
		defer iter.Stop()
		found := false
		for {
			doc, err := iter.Next()
			if errors.Is(err, iterator.Done) {
				break
			}
			if err != nil {
				return ErrProjectPermissionUnavailable
			}
			project, ok := projectFromFirestoreDocument(userID, doc.Ref.ID, doc.Data())
			if ok && project.ID == projectID {
				if found {
					return ErrProjectPermissionDenied
				}
				found = true
			}
		}
		if !found {
			return ErrProjectPermissionDenied
		}
		return nil
	}
}

// ListOwnedProjectsPage returns a stable ID-sorted page from the same Firestore
// owner records used by the CLI project authorizer.
func ListOwnedProjectsPage(ctx context.Context, fs *firestore.Client, userID string, pageSize int, pageToken string) ([]ProjectSummary, string, error) {
	userID = strings.TrimSpace(userID)
	if fs == nil || !ValidPathSegment(userID) {
		return nil, "", ErrProjectPermissionUnavailable
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 100 {
		pageSize = 100
	}
	cursor := ""
	if pageToken != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(pageToken)
		if err != nil || !ValidPathSegment(string(decoded)) {
			return nil, "", ErrProjectPageTokenInvalid
		}
		cursor = string(decoded)
	}

	iter := fs.Collection("projects").Documents(ctx)
	defer iter.Stop()
	projectsByID := make(map[string]ProjectSummary)
	for {
		doc, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, "", ErrProjectPermissionUnavailable
		}
		project, ok := projectFromFirestoreDocument(userID, doc.Ref.ID, doc.Data())
		if ok {
			if existing, found := projectsByID[project.ID]; !found || project.Name < existing.Name {
				projectsByID[project.ID] = project
			}
		}
	}
	projects := make([]ProjectSummary, 0, len(projectsByID))
	for _, project := range projectsByID {
		projects = append(projects, project)
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].ID < projects[j].ID })
	start := 0
	if cursor != "" {
		for start < len(projects) && projects[start].ID <= cursor {
			start++
		}
	}
	end := start + pageSize
	if end > len(projects) {
		end = len(projects)
	}
	page := append([]ProjectSummary(nil), projects[start:end]...)
	next := ""
	if end < len(projects) && len(page) > 0 {
		next = base64.RawURLEncoding.EncodeToString([]byte(page[len(page)-1].ID))
	}
	return page, next, nil
}

func projectDocumentID(userID, projectID string) string { return userID + "_" + projectID }

func projectFromFirestoreDocument(userID, docID string, data map[string]interface{}) (ProjectSummary, bool) {
	userPrefix := userID + "_"
	if !strings.HasPrefix(docID, userPrefix) {
		return ProjectSummary{}, false
	}
	if storedOwner, exists := data["user_id"]; exists {
		owner, ok := storedOwner.(string)
		if !ok || owner != userID {
			return ProjectSummary{}, false
		}
	}
	suffix := strings.TrimPrefix(docID, userPrefix)
	projectID, _ := data["project_id"].(string)
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		projectID = strings.TrimSpace(suffix)
	}
	if !ValidPathSegment(projectID) {
		return ProjectSummary{}, false
	}
	if key, exists := data["idempotency_key"]; exists {
		idempotencyKey, ok := key.(string)
		if !ok || !ValidPathSegment(idempotencyKey) {
			return ProjectSummary{}, false
		}
		if suffix == idempotencyKey && suffix != projectID {
			return ProjectSummary{}, false
		}
	}
	name, _ := data["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		name = projectID
	}
	createdAt, _ := data["created_at"].(time.Time)
	return ProjectSummary{ID: projectID, Name: name, CreatedAt: createdAt}, true
}

// ProjectMiddleware extracts the project identifier from the X-Project-ID header
// and sets it in the Gin context as "projectID".
// Returns 400 if the header is missing or invalid.
func ProjectMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		project := strings.TrimSpace(c.GetHeader("X-Project-ID"))
		if !ValidPathSegment(project) {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid X-Project-ID header"})
			return
		}

		c.Set("projectID", project)
		c.Next()
	}
}
