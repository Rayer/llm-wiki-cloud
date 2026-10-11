package clirelease

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
)

var ErrObjectNotFound = errors.New("release object not found")

type ObjectStore interface {
	Read(context.Context, string) ([]byte, int64, error)
	Create(context.Context, string, []byte, string, string) (int64, error)
	Update(context.Context, string, []byte, int64, string, string) (int64, error)
}

type PublicAssetVerifier func(context.Context, string, int64, string) error

func Publish(ctx context.Context, store ObjectStore, manifest Manifest, releaseDir, publicBaseURL string, verify PublicAssetVerifier) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	if manifest.Channel == "dev" || manifest.Version == "dev" {
		return errors.New("dev builds cannot be published")
	}
	base, err := validatePublicBaseURL(publicBaseURL)
	if err != nil {
		return err
	}
	if verify == nil {
		return errors.New("public asset verifier is required")
	}
	checksums, err := os.ReadFile(path.Join(releaseDir, "checksums.txt"))
	if err != nil {
		return fmt.Errorf("read checksums: %w", err)
	}
	checksumLines := make([]string, 0, len(manifest.Assets))
	for _, asset := range manifest.Assets {
		checksumLines = append(checksumLines, fmt.Sprintf("%s  %s", asset.SHA256, asset.AssetName))
	}
	if !bytes.Equal(checksums, []byte(strings.Join(checksumLines, "\n")+"\n")) {
		return errors.New("checksums do not match the release manifest")
	}

	manifestBytes, err := makePublishedManifest(ctx, store, manifest, releaseDir, base, verify)
	if err != nil {
		return err
	}
	if err := createOrMatch(ctx, store, path.Join("cli", "versions", manifest.Version, "checksums.txt"), checksums, "text/plain; charset=utf-8", "public,max-age=31536000,immutable"); err != nil {
		return err
	}
	manifestKey := path.Join("cli", "versions", manifest.Version, "manifest.json")
	if err := createOrMatch(ctx, store, manifestKey, manifestBytes, "application/json", "public,max-age=31536000,immutable"); err != nil {
		return err
	}
	if manifest.Channel != "stable" {
		return nil
	}
	return updateStableLatest(ctx, store, manifestBytes, manifest.Version)
}

func makePublishedManifest(ctx context.Context, store ObjectStore, manifest Manifest, releaseDir, base string, verify PublicAssetVerifier) ([]byte, error) {
	manifest.Assets = append([]Asset(nil), manifest.Assets...)
	for index := range manifest.Assets {
		asset := &manifest.Assets[index]
		archive, err := os.ReadFile(path.Join(releaseDir, asset.AssetName))
		if err != nil {
			return nil, fmt.Errorf("read archive %q: %w", asset.AssetName, err)
		}
		if int64(len(archive)) != asset.Size || sha256Hex(archive) != asset.SHA256 {
			return nil, fmt.Errorf("archive %q does not match manifest size and SHA-256", asset.AssetName)
		}
		objectKey := path.Join("cli", "versions", manifest.Version, asset.AssetName)
		if err := createOrMatch(ctx, store, objectKey, archive, "application/octet-stream", "public,max-age=31536000,immutable"); err != nil {
			return nil, err
		}
		asset.DownloadURL = base + "/" + objectKey
		if err := verify(ctx, asset.DownloadURL, asset.Size, asset.SHA256); err != nil {
			return nil, fmt.Errorf("verify public download for %q: %w", asset.AssetName, err)
		}
	}
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	return json.MarshalIndent(manifest, "", "  ")
}

func createOrMatch(ctx context.Context, store ObjectStore, key string, data []byte, contentType, cacheControl string) error {
	existing, _, err := store.Read(ctx, key)
	if err == nil {
		if bytes.Equal(existing, data) {
			return nil
		}
		return fmt.Errorf("version object %q already exists with different bytes", key)
	}
	if !errors.Is(err, ErrObjectNotFound) {
		return fmt.Errorf("read %q before conditional create: %w", key, err)
	}
	if _, err := store.Create(ctx, key, data, contentType, cacheControl); err != nil {
		readback, _, readErr := store.Read(ctx, key)
		if readErr == nil && bytes.Equal(readback, data) {
			return nil
		}
		return fmt.Errorf("conditionally create %q: %w", key, err)
	}
	readback, _, err := store.Read(ctx, key)
	if err != nil || !bytes.Equal(readback, data) {
		return fmt.Errorf("readback for %q does not match uploaded bytes", key)
	}
	return nil
}

func updateStableLatest(ctx context.Context, store ObjectStore, manifestBytes []byte, version string) error {
	const key = "cli/stable/latest.json"
	current, generation, err := store.Read(ctx, key)
	if errors.Is(err, ErrObjectNotFound) {
		if _, err := store.Create(ctx, key, manifestBytes, "application/json", "no-store"); err != nil {
			readback, _, readErr := store.Read(ctx, key)
			if readErr == nil && bytes.Equal(readback, manifestBytes) {
				return nil
			}
			return fmt.Errorf("conditionally create stable latest: %w", err)
		}
		readback, _, err := store.Read(ctx, key)
		if err != nil || !bytes.Equal(readback, manifestBytes) {
			return errors.New("stable latest readback differs after create")
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read stable latest: %w", err)
	}
	var latest Manifest
	if err := json.Unmarshal(current, &latest); err != nil || latest.Validate() != nil || latest.Channel != "stable" {
		return errors.New("existing stable latest metadata is invalid")
	}
	order, err := CompareVersions(latest.Version, version)
	if err != nil {
		return err
	}
	if order > 0 {
		return fmt.Errorf("refusing to replace newer stable version %s with %s", latest.Version, version)
	}
	if order == 0 {
		if bytes.Equal(current, manifestBytes) {
			return nil
		}
		return fmt.Errorf("stable version %s already points to different manifest bytes", version)
	}
	if _, err := store.Update(ctx, key, manifestBytes, generation, "application/json", "no-store"); err != nil {
		return fmt.Errorf("generation-conditional stable latest update failed: %w", err)
	}
	readback, _, err := store.Read(ctx, key)
	if err != nil || !bytes.Equal(readback, manifestBytes) {
		return errors.New("stable latest readback differs after generation update")
	}
	return nil
}

func validatePublicBaseURL(value string) (string, error) {
	parsed, err := url.ParseRequestURI(strings.TrimRight(value, "/"))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("public base URL must be an HTTPS origin or path without credentials, query, or fragment")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func VerifyPublicAsset(ctx context.Context, assetURL string, size int64, wantSHA256 string) error {
	const browserOrigin = "https://wiki.dev.rayer.idv.tw"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", "lwc-cli-release-verifier")
	request.Header.Set("Origin", browserOrigin)
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned HTTP %d", response.StatusCode)
	}
	allowOrigin := response.Header.Get("Access-Control-Allow-Origin")
	if allowOrigin != browserOrigin && allowOrigin != "*" {
		return errors.New("public download does not allow the DEV wiki origin")
	}
	hasher := sha256.New()
	count, err := io.Copy(hasher, io.LimitReader(response.Body, size+1))
	if err != nil {
		return err
	}
	if count != size || hex.EncodeToString(hasher.Sum(nil)) != wantSHA256 {
		return errors.New("public download bytes do not match the release archive")
	}
	return nil
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
