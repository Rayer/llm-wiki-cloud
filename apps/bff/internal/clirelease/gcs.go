package clirelease

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"cloud.google.com/go/storage"
)

type GCSStore struct {
	bucket *storage.BucketHandle
}

func NewGCSStore(client *storage.Client, bucket string) *GCSStore {
	return &GCSStore{bucket: client.Bucket(bucket)}
}

func (store *GCSStore) Read(ctx context.Context, key string) ([]byte, int64, error) {
	reader, err := store.bucket.Object(key).NewReader(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return nil, 0, ErrObjectNotFound
	}
	if err != nil {
		return nil, 0, err
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, 0, err
	}
	if int64(len(data)) != reader.Attrs.Size {
		return nil, 0, errors.New("release object read size differs from GCS attributes")
	}
	return data, reader.Attrs.Generation, nil
}

func (store *GCSStore) Create(ctx context.Context, key string, data []byte, contentType, cacheControl string) (int64, error) {
	uploadCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	object := store.bucket.Object(key).If(storage.Conditions{DoesNotExist: true})
	writer := object.NewWriter(uploadCtx)
	writer.ContentType = contentType
	writer.CacheControl = cacheControl
	if _, err := io.Copy(writer, bytes.NewReader(data)); err != nil {
		cancel()
		_ = writer.Close()
		return 0, err
	}
	if err := writer.Close(); err != nil {
		return 0, err
	}
	attrs := writer.Attrs()
	if attrs == nil {
		return 0, fmt.Errorf("GCS did not return attributes for %q", key)
	}
	return attrs.Generation, nil
}

func (store *GCSStore) Update(ctx context.Context, key string, data []byte, generation int64, contentType, cacheControl string) (int64, error) {
	uploadCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	object := store.bucket.Object(key).If(storage.Conditions{GenerationMatch: generation})
	writer := object.NewWriter(uploadCtx)
	writer.ContentType = contentType
	writer.CacheControl = cacheControl
	if _, err := io.Copy(writer, bytes.NewReader(data)); err != nil {
		cancel()
		_ = writer.Close()
		return 0, err
	}
	if err := writer.Close(); err != nil {
		return 0, err
	}
	attrs := writer.Attrs()
	if attrs == nil {
		return 0, fmt.Errorf("GCS did not return attributes for %q", key)
	}
	return attrs.Generation, nil
}
