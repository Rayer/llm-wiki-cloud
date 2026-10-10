package gcs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/storage"
	store "github.com/rayer/llm-wiki-bff/internal/storage"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRawSyncGCSStorageEmulatorStreamingAndGenerationCAS(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("STORAGE_EMULATOR_HOST"))
	if endpoint == "" {
		t.Skip("requires a loopback GCS storage emulator")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		t.Fatal("STORAGE_EMULATOR_HOST must be an http loopback origin")
	}
	host, _, err := net.SplitHostPort(u.Host)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.IsLoopback() {
		t.Fatal("STORAGE_EMULATOR_HOST must resolve to a loopback address")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	admin, err := storage.NewClient(ctx)
	if err != nil {
		t.Fatalf("create emulator client: %v", err)
	}
	defer admin.Close()
	bucketName := fmt.Sprintf("lwc378-rawsync-%d", time.Now().UnixNano())
	if err := admin.Bucket(bucketName).Create(ctx, "lwc378-local", nil); err != nil {
		t.Fatalf("create disposable emulator bucket: %v", err)
	}
	defer func() {
		it := admin.Bucket(bucketName).Objects(ctx, &storage.Query{})
		for {
			attrs, err := it.Next()
			if errors.Is(err, iterator.Done) {
				break
			}
			if err != nil {
				t.Errorf("list emulator objects for cleanup: %v", err)
				break
			}
			if err := admin.Bucket(bucketName).Object(attrs.Name).Delete(ctx); err != nil && !errors.Is(err, storage.ErrObjectNotExist) {
				t.Errorf("delete emulator object %q: %v", attrs.Name, err)
			}
		}
		if err := admin.Bucket(bucketName).Delete(ctx); err != nil {
			t.Errorf("delete disposable emulator bucket: %v", err)
		}
	}()

	t.Setenv("LOCAL_CLOUD_SCOPE", "")
	client, err := NewClient(bucketName)
	if err != nil {
		t.Fatalf("create raw sync GCS client: %v", err)
	}
	defer client.Close()
	project := client.WithScope("lwc378-user", "lwc378-project")
	rel := "nested/attachments/streamed.bin"
	first := bytes.Repeat([]byte("gcs-emulator-stream-"), 81920)
	firstDigest := sha256.Sum256(first)
	firstGeneration, err := project.WriteSyncRawFile(ctx, rel, bytes.NewReader(first), "", hex.EncodeToString(firstDigest[:]))
	if err != nil {
		t.Fatalf("create raw object through staging and CopierFrom: %v", err)
	}
	if _, err := strconv.ParseInt(firstGeneration, 10, 64); err != nil {
		t.Fatalf("first raw generation %q is not numeric: %v", firstGeneration, err)
	}
	objectName := project.prefix() + "/raw/" + rel
	firstAttrs, err := admin.Bucket(bucketName).Object(objectName).Attrs(ctx)
	if err != nil {
		t.Fatalf("read first raw object metadata: %v", err)
	}
	if strconv.FormatInt(firstAttrs.Generation, 10) != firstGeneration || firstAttrs.Size != int64(len(first)) || firstAttrs.Metadata["sha256"] != hex.EncodeToString(firstDigest[:]) {
		t.Fatalf("first raw object attrs=%#v generation=%s", firstAttrs, firstGeneration)
	}

	second := bytes.Repeat([]byte("updated-generation-"), 90000)
	secondDigest := sha256.Sum256(second)
	secondGeneration, err := project.WriteSyncRawFile(ctx, rel, bytes.NewReader(second), firstGeneration, hex.EncodeToString(secondDigest[:]))
	if err != nil || secondGeneration == firstGeneration {
		t.Fatalf("generation matched streaming replacement generation=%q err=%v", secondGeneration, err)
	}
	mediaURL := strings.TrimRight(endpoint, "/") + "/download/storage/v1/b/" + url.PathEscape(bucketName) + "/o/" + url.PathEscape(objectName) + "?alt=media&generation=" + url.QueryEscape(secondGeneration)
	response, err := http.Get(mediaURL)
	if err != nil {
		t.Fatalf("read emulator media endpoint: %v", err)
	}
	readback, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || response.StatusCode != http.StatusOK || !bytes.Equal(readback, second) {
		t.Fatalf("emulator media read status=%d bytes=%d read=%v close=%v", response.StatusCode, len(readback), readErr, closeErr)
	}
	badDigest := strings.Repeat("0", 64)
	if _, err := project.WriteSyncRawFile(ctx, "never-publish.bin", bytes.NewReader([]byte("wrong digest")), "", badDigest); err == nil {
		t.Fatal("digest-mismatched staging object was published")
	}
	if _, err := admin.Bucket(bucketName).Object(project.prefix() + "/raw/never-publish.bin").Attrs(ctx); err == nil {
		t.Fatal("digest mismatch created a raw destination object")
	}

	// Probe the emulator's raw SDK copy precondition directly so an incapable
	// backend is distinguished from a product CAS regression.
	probeSource := admin.Bucket(bucketName).Object("lwc378-cas-probe-source")
	probeDestination := admin.Bucket(bucketName).Object("lwc378-cas-probe-destination")
	writeProbeObject := func(object *storage.ObjectHandle, body string) {
		t.Helper()
		writer := object.NewWriter(ctx)
		if _, err := io.WriteString(writer, body); err != nil {
			_ = writer.Close()
			t.Fatalf("write CAS capability probe object: %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("close CAS capability probe object: %v", err)
		}
	}
	writeProbeObject(probeSource, "copy source")
	writeProbeObject(probeDestination, "original destination")
	staleAttrs, err := probeDestination.Attrs(ctx)
	if err != nil {
		t.Fatalf("read original CAS probe destination: %v", err)
	}
	writeProbeObject(probeDestination, "replacement destination")
	currentAttrs, err := probeDestination.Attrs(ctx)
	if err != nil || currentAttrs.Generation == staleAttrs.Generation {
		t.Fatalf("replace CAS probe destination generation: old=%d current=%v err=%v", staleAttrs.Generation, currentAttrs, err)
	}
	_, probeErr := probeDestination.If(storage.Conditions{GenerationMatch: staleAttrs.Generation}).CopierFrom(probeSource).Run(ctx)
	if probeErr == nil {
		t.Skip("emulator accepts a stale destination GenerationMatch on direct SDK CopierFrom; raw generation CAS is unsupported")
	}
	if !isRawSyncGenerationPreconditionFailure(probeErr) {
		t.Fatalf("direct SDK stale-generation probe failed without a precondition response: %v", probeErr)
	}

	if _, err := project.WriteSyncRawFile(ctx, rel, bytes.NewReader(first), firstGeneration, hex.EncodeToString(firstDigest[:])); err == nil {
		t.Fatal("raw sync accepted a stale generation although the SDK capability probe proved the emulator supports it")
	} else if !errors.Is(err, store.ErrRawSyncConflict) {
		t.Fatalf("stale generation write error=%v, want conflict", err)
	}
	latestAttrs, err := admin.Bucket(bucketName).Object(objectName).Attrs(ctx)
	if err != nil || strconv.FormatInt(latestAttrs.Generation, 10) != secondGeneration || latestAttrs.Size != int64(len(second)) || latestAttrs.Metadata["sha256"] != hex.EncodeToString(secondDigest[:]) {
		t.Fatalf("post-CAS attrs=%#v generation=%s err=%v", latestAttrs, secondGeneration, err)
	}
}

func isRawSyncGenerationPreconditionFailure(err error) bool {
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) && apiErr.Code == http.StatusPreconditionFailed {
		return true
	}
	code := status.Code(err)
	return code == codes.FailedPrecondition || code == codes.Aborted
}
