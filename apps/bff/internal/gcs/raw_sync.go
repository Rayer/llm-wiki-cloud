package gcs

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"cloud.google.com/go/storage"
	store "github.com/rayer/llm-wiki-bff/internal/storage"
)

func (c *Client) ListSyncRawFiles(ctx context.Context) ([]store.RawSyncFile, error) {
	prefix := c.prefix() + "/raw/"
	files := make([]store.RawSyncFile, 0)
	seen := make(map[string]struct{})
	var total int64
	err := c.visitObjectsRaw(ctx, prefix, false, func(object backendObject) error {
		if object.Name == "" || !strings.HasPrefix(object.Name, prefix) {
			return errors.New("raw object listing returned an invalid path")
		}
		rel := strings.TrimPrefix(object.Name, prefix)
		if rel == "" || strings.HasSuffix(rel, "/") { // GCS directory marker, not a regular file.
			return nil
		}
		if err := store.ValidateRawSyncPath(rel); err != nil || object.Generation <= 0 || object.Size < 0 || object.Size > store.MaxRawSyncFileBytes {
			return errors.New("raw object listing contains an unsupported file")
		}
		if _, duplicate := seen[rel]; duplicate {
			return errors.New("raw object listing returned a duplicate path")
		}
		seen[rel] = struct{}{}
		if len(files) >= store.MaxRawSyncFiles || total > store.MaxRawSyncTotalBytes-object.Size {
			return errors.New("raw object listing exceeds sync limits")
		}
		reader, size, err := c.OpenSyncRawFile(ctx, rel, strconv.FormatInt(object.Generation, 10))
		if err != nil {
			return fmt.Errorf("read raw object digest: %w", err)
		}
		hasher := sha256.New()
		count, readErr := io.Copy(hasher, io.LimitReader(reader, store.MaxRawSyncFileBytes+1))
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil || count != size || size != object.Size || count > store.MaxRawSyncFileBytes {
			return errors.New("raw object digest could not be verified")
		}
		digest := hex.EncodeToString(hasher.Sum(nil))
		files = append(files, store.RawSyncFile{Path: rel, Size: object.Size, SHA256: digest, Generation: strconv.FormatInt(object.Generation, 10)})
		total += object.Size
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func (c *Client) OpenSyncRawFile(ctx context.Context, rel, generation string) (io.ReadCloser, int64, error) {
	if err := store.ValidateRawSyncPath(rel); err != nil {
		return nil, 0, err
	}
	version, err := strconv.ParseInt(generation, 10, 64)
	if err != nil || version <= 0 {
		return nil, 0, errors.New("invalid raw object generation")
	}
	name := c.prefix() + "/raw/" + rel
	if c.backend != nil {
		object, err := c.readObject(ctx, name, version, store.MaxRawSyncFileBytes)
		if err != nil {
			return nil, 0, err
		}
		if object.Generation != version {
			return nil, 0, errors.New("raw object generation changed")
		}
		return io.NopCloser(bytes.NewReader(object.Data)), object.Size, nil
	}
	reader, err := c.bucket.Object(name).Generation(version).NewReader(ctx)
	if err != nil {
		if objectNotFound(err) {
			return nil, 0, storage.ErrObjectNotExist
		}
		return nil, 0, fmt.Errorf("open raw object: %w", err)
	}
	if reader.Attrs.Generation != version || reader.Attrs.Size < 0 || reader.Attrs.Size > store.MaxRawSyncFileBytes {
		_ = reader.Close()
		return nil, 0, errors.New("raw object attributes changed")
	}
	return reader, reader.Attrs.Size, nil
}

func (c *Client) WriteSyncRawFile(ctx context.Context, rel string, body io.Reader, expectedGeneration, expectedSHA256 string) (string, error) {
	if err := store.ValidateRawSyncPath(rel); err != nil || !store.ValidRawSyncSHA256(expectedSHA256) {
		return "", errors.New("invalid raw sync upload")
	}
	expected := int64(0)
	if expectedGeneration != "" {
		value, err := strconv.ParseInt(expectedGeneration, 10, 64)
		if err != nil || value <= 0 {
			return "", errors.New("invalid expected raw object generation")
		}
		expected = value
	}
	name := c.prefix() + "/raw/" + rel
	if c.backend != nil {
		data, err := io.ReadAll(io.LimitReader(body, store.MaxRawSyncFileBytes+1))
		if err != nil {
			return "", err
		}
		if int64(len(data)) > store.MaxRawSyncFileBytes {
			return "", errors.New("raw sync file exceeds the per-file limit")
		}
		digest := fmt.Sprintf("%x", sha256.Sum256(data))
		if digest != expectedSHA256 {
			return "", errors.New("raw sync upload digest did not match the listed local file")
		}
		attrs, err := c.writeObject(ctx, name, data, contentTypeForPath("raw/"+rel), map[string]string{"sha256": digest}, createOrGenerationCondition(expected))
		if errors.Is(conditionalWriteError(err), store.ErrGenerationMismatch) {
			return "", store.ErrRawSyncConflict
		}
		if err != nil {
			return "", fmt.Errorf("%w: %w", store.ErrRawSyncCommitUncertain, err)
		}
		return strconv.FormatInt(attrs.Generation, 10), nil
	}

	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", errors.New("could not create temporary raw sync object")
	}
	temporaryName := c.prefix() + "/.lwc-sync-staging/" + hex.EncodeToString(random)
	temporary := c.bucket.Object(temporaryName)
	writer := temporary.If(storage.Conditions{DoesNotExist: true}).NewWriter(ctx)
	writer.ContentType = contentTypeForPath("raw/" + rel)
	writer.ChunkSize = 1 << 20
	hasher := sha256.New()
	count, copyErr := io.Copy(io.MultiWriter(writer, hasher), io.LimitReader(body, store.MaxRawSyncFileBytes+1))
	if copyErr == nil && count > store.MaxRawSyncFileBytes {
		copyErr = errors.New("raw sync file exceeds the per-file limit")
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	if copyErr == nil && digest != expectedSHA256 {
		copyErr = errors.New("raw sync upload digest did not match the listed local file")
	}
	if copyErr != nil {
		_ = writer.CloseWithError(copyErr)
		return "", copyErr
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	temporaryAttrs := writer.Attrs()
	if temporaryAttrs == nil || temporaryAttrs.Generation <= 0 {
		return "", errors.New("temporary raw sync object has no generation")
	}
	defer bestEffortTemporaryCleanup(temporaryAttrs.Generation, func(ctx context.Context) error {
		return temporary.If(temporaryObjectDeleteConditions(temporaryAttrs.Generation)).Delete(ctx)
	})

	destination := c.bucket.Object(name)
	if expected == 0 {
		destination = destination.If(storage.Conditions{DoesNotExist: true})
	} else {
		destination = destination.If(storage.Conditions{GenerationMatch: expected})
	}
	copier := destination.CopierFrom(temporary.Generation(temporaryAttrs.Generation))
	copier.ContentType = contentTypeForPath("raw/" + rel)
	copier.Metadata = map[string]string{"sha256": digest}
	attrs, err := copier.Run(ctx)
	if errors.Is(conditionalWriteError(err), store.ErrGenerationMismatch) {
		return "", store.ErrRawSyncConflict
	}
	if err != nil {
		return "", fmt.Errorf("%w: %w", store.ErrRawSyncCommitUncertain, err)
	}
	return strconv.FormatInt(attrs.Generation, 10), nil
}
