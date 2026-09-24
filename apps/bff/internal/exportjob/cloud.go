package exportjob

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/iamcredentials/v1"
	"google.golang.org/api/option"
)

// CloudArchiveStore keeps archive bytes private in GCS and signs direct
// browser downloads with the runtime service account's IAM SignBlob capability.
type CloudArchiveStore struct {
	client   *storage.Client
	bucket   string
	signerID string
	iam      *iamcredentials.Service
}

func NewCloudArchiveStore(ctx context.Context, bucket, signerID string) (*CloudArchiveStore, error) {
	if strings.TrimSpace(bucket) == "" || strings.TrimSpace(signerID) == "" {
		return nil, errors.New("export storage configuration is incomplete")
	}
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("create export storage client: %w", err)
	}
	iam, err := iamcredentials.NewService(ctx, option.WithScopes(iamcredentials.CloudPlatformScope))
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("create export signing client: %w", err)
	}
	return &CloudArchiveStore{client: client, bucket: bucket, signerID: signerID, iam: iam}, nil
}

func (s *CloudArchiveStore) Close() error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Close()
}

func (s *CloudArchiveStore) Stat(ctx context.Context, userID, projectID, exportID string) (ArchiveInfo, error) {
	name, err := ReadyObject(userID, projectID, exportID)
	if err != nil {
		return ArchiveInfo{}, err
	}
	attrs, err := s.client.Bucket(s.bucket).Object(name).Attrs(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return ArchiveInfo{}, ErrArchiveMissing
	}
	if err != nil {
		return ArchiveInfo{}, err
	}
	return ArchiveInfo{Size: attrs.Size, CustomTime: attrs.CustomTime.UTC()}, nil
}

func (s *CloudArchiveStore) Sign(ctx context.Context, userID, projectID, exportID string, expiry time.Time, filename string) (string, error) {
	name, err := ReadyObject(userID, projectID, exportID)
	if err != nil {
		return "", err
	}
	serviceAccount := "projects/-/serviceAccounts/" + s.signerID
	return storage.SignedURL(s.bucket, name, &storage.SignedURLOptions{
		Scheme: storage.SigningSchemeV4, Method: http.MethodGet, Expires: expiry,
		GoogleAccessID:  s.signerID,
		QueryParameters: url.Values{"response-content-disposition": {safeContentDisposition(filename)}},
		SignBytes: func(data []byte) ([]byte, error) {
			signCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			result, err := s.iam.Projects.ServiceAccounts.SignBlob(serviceAccount, &iamcredentials.SignBlobRequest{Payload: base64.StdEncoding.EncodeToString(data)}).Context(signCtx).Do()
			if err != nil {
				return nil, err
			}
			return base64.StdEncoding.DecodeString(result.SignedBlob)
		},
	})
}

// CloudRunJobStarter invokes one preconfigured Cloud Run Job. The API only
// sends identity and scope; it never carries data or archive bytes.
type CloudRunJobStarter struct {
	url         string
	client      *http.Client
	tokenSource oauth2.TokenSource
}

func NewCloudRunJobStarter(jobURL string) *CloudRunJobStarter {
	return &CloudRunJobStarter{url: strings.TrimSpace(jobURL)}
}

func safeDispositionFilename(filename string) string {
	var b strings.Builder
	for _, r := range filename {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_.", r) {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "project_export.zip"
	}
	return b.String()
}

func safeContentDisposition(filename string) string {
	return `attachment; filename="` + safeDispositionFilename(filename) + `"; filename*=UTF-8''` + url.PathEscape(filename)
}

func (s *CloudRunJobStarter) Start(ctx context.Context, userID, projectID string, job Job) error {
	parsed, err := url.Parse(s.url)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "run.googleapis.com" || !strings.HasSuffix(parsed.Path, ":run") {
		return errors.New("export job is unavailable")
	}
	if !validSegment(userID) || !validSegment(projectID) || !validSegment(job.ExportID) || !job.Scope.Valid() {
		return errors.New("export job request is invalid")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tokenSource := s.tokenSource
	if tokenSource == nil {
		tokenSource, err = google.DefaultTokenSource(ctx, "https://www.googleapis.com/auth/cloud-platform")
		if err != nil {
			return errors.New("export job is unavailable")
		}
	}
	accessToken, err := tokenSource.Token()
	if err != nil {
		return errors.New("export job is unavailable")
	}
	payload, err := json.Marshal(map[string]any{"overrides": map[string]any{"containerOverrides": []any{map[string]any{"env": []any{
		map[string]string{"name": "EXPORT_USER_ID", "value": userID},
		map[string]string{"name": "EXPORT_PROJECT_ID", "value": projectID},
		map[string]string{"name": "EXPORT_ID", "value": job.ExportID},
		map[string]string{"name": "EXPORT_SCOPE", "value": string(job.Scope)},
	}}}}})
	if err != nil {
		return errors.New("export job is unavailable")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, strings.NewReader(string(payload)))
	if err != nil {
		return errors.New("export job is unavailable")
	}
	request.Header.Set("Authorization", "Bearer "+accessToken.AccessToken)
	request.Header.Set("Content-Type", "application/json")
	client := s.client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("export job is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return errors.New("export job is unavailable")
	}
	return nil
}

var _ ArchiveStore = (*CloudArchiveStore)(nil)
var _ JobStarter = (*CloudRunJobStarter)(nil)
