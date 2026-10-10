package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	store "github.com/rayer/llm-wiki-bff/internal/storage"
)

const (
	cliRawListRoute = "/api/v1/sync/raw"
	cliRawFileRoute = "/api/v1/sync/raw/file"
	cliRawPageSize  = 100
)

type syncServiceLocator struct {
	Origin string `json:"origin"`
}

type syncDataAPIClient struct {
	origin     string
	httpClient *http.Client
	authClient *cliAPIClient
	binding    vaultBindingConfig
}

type syncAPIResponse struct {
	status int
	header http.Header
	body   io.ReadCloser
}

type syncDataStatusError struct{ status int }

func (e syncDataStatusError) Error() string {
	return fmt.Sprintf("Sync data service returned HTTP %d", e.status)
}

type remoteRawPage struct {
	Files         []store.RawSyncFile `json:"files"`
	TotalFiles    int                 `json:"total_files"`
	Snapshot      string              `json:"snapshot"`
	NextPageToken string              `json:"next_page_token"`
}

type localRawFile struct {
	store.RawSyncFile
	SourceInfo os.FileInfo
}

type localRawInventory struct {
	RootPath string
	Root     *os.Root
	RootInfo os.FileInfo
	Files    []localRawFile
	Total    int64
}

func (inventory *localRawInventory) Close() error {
	if inventory == nil || inventory.Root == nil {
		return nil
	}
	return inventory.Root.Close()
}

type rawPushResult struct {
	added, updated, skipped, downloaded int
	failed                              []string
}

func runRawInit(args []string) error {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	host := flags.String("host", "", "Auth/control-plane origin")
	vault := flags.String("vault", "", "path to the local wiki vault")
	projectID := flags.String("project-id", "", "Cloud Project ID to authorize")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*vault) == "" || !validVaultID(*projectID) {
		return errors.New("usage: lwc-sync init --vault PATH --host AUTH_ORIGIN --project-id ID")
	}
	store, err := defaultLocalAuthStore()
	if err != nil {
		return err
	}
	origin, err := selectAuthHost(store, *host)
	if err != nil {
		return err
	}
	binding, err := prepareVaultForInit(*vault, origin, *projectID)
	if err != nil {
		return err
	}
	return withStoredCredentials(origin, func(client *cliAPIClient, _ *cliLocalCredentials) error {
		var current struct {
			Bindings []authSyncBinding `json:"bindings"`
		}
		if err := client.request(context.Background(), http.MethodGet, "/api/v1/auth/cli/bindings", nil, &current); err != nil {
			return err
		}
		for _, item := range current.Bindings {
			if item.ProjectID != binding.ProjectID {
				continue
			}
			if item.WikiID != binding.WikiID || item.Host != binding.Host {
				return errors.New("server binding belongs to a different wiki or host; review it and run `lwc-sync binding reauthorize` explicitly")
			}
			if item.Status != "active" {
				return errors.New("this vault binding is revoked; run `lwc-sync binding reauthorize --vault PATH --project-id ID` after reviewing the account binding")
			}
			if binding.BindingID != "" && item.ID != binding.BindingID {
				return errors.New("server binding changed; run `lwc-sync binding reauthorize --vault PATH --project-id ID` explicitly")
			}
			if binding.BindingID == "" {
				if err := saveVaultBindingID(*vault, binding, item.ID); err != nil {
					return errors.New("an active server binding was found, but vault metadata could not be updated; rerun init to recover")
				}
			}
			fmt.Printf("Initialized raw sync for Project %s at %s.\n", binding.ProjectID, filepath.Clean(*vault))
			return nil
		}
		if binding.BindingID != "" {
			return errors.New("vault binding is no longer current; run `lwc-sync binding reauthorize --vault PATH --project-id ID` explicitly")
		}
		var created struct {
			ID string `json:"binding_id"`
		}
		body := map[string]string{"project_id": binding.ProjectID, "wiki_id": binding.WikiID, "host": binding.Host}
		if err := client.request(context.Background(), http.MethodPost, "/api/v1/auth/cli/bindings", body, &created); err != nil {
			return err
		}
		if !validVaultID(created.ID) {
			return errors.New("Auth service returned an invalid sync binding")
		}
		if err := saveVaultBindingID(*vault, binding, created.ID); err != nil {
			return errors.New("server binding was created but vault metadata could not be saved; rerun init to recover the existing binding")
		}
		fmt.Printf("Initialized raw sync for Project %s at %s.\n", binding.ProjectID, filepath.Clean(*vault))
		return nil
	})
}

func runRawPush(args []string) error {
	flags := flag.NewFlagSet("push", flag.ContinueOnError)
	vault := flags.String("vault", "", "path to the local wiki vault")
	pushAndSync := flags.Bool("sync", false, "pull remote-only raw files after every local push succeeds")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*vault) == "" {
		return errors.New("usage: lwc-sync push --vault PATH [--sync]")
	}
	binding, err := loadVaultBinding(*vault)
	if err != nil {
		return errors.New("initialize this vault first with `lwc-sync init --vault PATH --host AUTH_ORIGIN --project-id ID`")
	}
	if binding.BindingID == "" {
		return errors.New("this vault has no active sync binding; run `lwc-sync init` or `lwc-sync binding reauthorize`")
	}
	return withStoredCredentials(binding.Host, func(authClient *cliAPIClient, _ *cliLocalCredentials) error {
		local, err := scanLocalRawFiles(context.Background(), *vault)
		if err != nil {
			return err
		}
		defer local.Close()
		var locator syncServiceLocator
		if err := authClient.request(context.Background(), http.MethodGet, "/api/v1/auth/cli/sync-service", nil, &locator); err != nil {
			return err
		}
		dataClient, err := newSyncDataAPIClient(locator.Origin, authClient, binding)
		if err != nil {
			return err
		}
		remote, err := dataClient.listAll(context.Background())
		if err != nil {
			return err
		}
		result := rawPushResult{failed: make([]string, 0)}
		if err := validateRawSyncUnion(local.Files, remote); err != nil {
			return err
		}
		capacityCount, capacityBytes, err := rawSyncRemoteUsage(remote)
		if err != nil {
			return err
		}
		uploadFiles := append([]localRawFile(nil), local.Files...)
		sort.SliceStable(uploadFiles, func(i, j int) bool {
			left, leftExists := remote[uploadFiles[i].Path]
			right, rightExists := remote[uploadFiles[j].Path]
			leftShrinks := leftExists && uploadFiles[i].Size < left.Size
			rightShrinks := rightExists && uploadFiles[j].Size < right.Size
			return leftShrinks && !rightShrinks
		})
		for _, file := range uploadFiles {
			if err := verifyLocalRawFile(local, file); err != nil {
				result.failed = append(result.failed, file.Path+": local source changed after inventory")
				fmt.Printf("failed %s\n", file.Path)
				continue
			}
			remoteFile, exists := remote[file.Path]
			if exists && remoteFile.SHA256 == file.SHA256 {
				result.skipped++
				fmt.Printf("skip   %s\n", file.Path)
				continue
			}
			nextCount := capacityCount
			nextBytes := capacityBytes
			if exists {
				nextBytes -= remoteFile.Size
			} else {
				nextCount++
			}
			if nextCount > store.MaxRawSyncFiles || nextBytes > store.MaxRawSyncTotalBytes-file.Size {
				result.failed = append(result.failed, file.Path+": raw tree capacity remains occupied because an earlier shrink did not complete")
				fmt.Printf("failed %s\n", file.Path)
				continue
			}
			if exists {
				file.Generation = remoteFile.Generation
			}
			err := dataClient.uploadFile(context.Background(), local, file)
			capacityReconciled := false
			if err != nil && isAmbiguousTransfer(err) {
				readback, readErr := dataClient.listAll(context.Background())
				if readErr != nil {
					capacityCount, capacityBytes = reserveRawSyncPossibleUsage(capacityCount, capacityBytes, exists, remoteFile.Size, file.Size)
					result.failed = append(result.failed, file.Path+": ambiguous upload response; capacity was reserved and remaining writes stopped")
					fmt.Printf("failed %s\n", file.Path)
					break
				}
				readCount, readBytes, usageErr := rawSyncRemoteUsage(readback)
				if usageErr != nil {
					capacityCount, capacityBytes = reserveRawSyncPossibleUsage(capacityCount, capacityBytes, exists, remoteFile.Size, file.Size)
					result.failed = append(result.failed, file.Path+": ambiguous upload response; remote capacity could not be validated and remaining writes stopped")
					fmt.Printf("failed %s\n", file.Path)
					break
				}
				capacityCount, capacityBytes = readCount, readBytes
				if confirmed, ok := readback[file.Path]; ok && confirmed.SHA256 == file.SHA256 {
					err = nil
					capacityReconciled = true
				} else {
					result.failed = append(result.failed, file.Path+": ambiguous upload response; readback did not confirm the write and remaining writes stopped")
					fmt.Printf("failed %s\n", file.Path)
					break
				}
			}
			if err != nil {
				result.failed = append(result.failed, file.Path+": "+err.Error())
				fmt.Printf("failed %s\n", file.Path)
				continue
			}
			// A successful response or matching readback confirms remote occupancy,
			// even if the local source check below prevents a success count.
			if !capacityReconciled {
				capacityCount, capacityBytes = nextCount, nextBytes+file.Size
			}
			if err := verifyLocalRawFile(local, file); err != nil {
				result.failed = append(result.failed, file.Path+": local source changed during upload")
				fmt.Printf("failed %s\n", file.Path)
				continue
			}
			if exists {
				result.updated++
				fmt.Printf("update %s\n", file.Path)
			} else {
				result.added++
				fmt.Printf("add    %s\n", file.Path)
			}
		}
		if *pushAndSync && len(result.failed) == 0 {
			freshLocal, err := scanLocalRawFiles(context.Background(), *vault)
			if err == nil {
				defer freshLocal.Close()
			}
			if err == nil && !sameLocalRawInventory(local, freshLocal) {
				err = errors.New("local raw tree changed after inventory; rerun the command")
			}
			if err != nil {
				result.failed = append(result.failed, "local inventory changed: "+err.Error())
			}
			if len(result.failed) == 0 {
				freshRemote, err := dataClient.listAll(context.Background())
				if err != nil {
					result.failed = append(result.failed, "remote inventory: "+err.Error())
				} else {
					localPaths := make(map[string]struct{}, len(local.Files))
					for _, file := range freshLocal.Files {
						localPaths[file.Path] = struct{}{}
					}
					remotePaths := make([]string, 0, len(freshRemote))
					for path := range freshRemote {
						remotePaths = append(remotePaths, path)
					}
					sort.Strings(remotePaths)
					for _, path := range remotePaths {
						file := freshRemote[path]
						if _, exists := localPaths[path]; exists {
							continue
						}
						if err := dataClient.downloadFile(context.Background(), *vault, file); err != nil {
							result.failed = append(result.failed, path+": "+err.Error())
							fmt.Printf("failed %s\n", path)
							continue
						}
						result.downloaded++
						fmt.Printf("pull   %s\n", path)
					}
				}
			}
		} else if *pushAndSync && len(result.failed) > 0 {
			fmt.Println("pull skipped because one or more local uploads failed")
		}
		fmt.Printf("Added: %d  Updated: %d  Skipped: %d  Downloaded: %d  Failed: %d\n", result.added, result.updated, result.skipped, result.downloaded, len(result.failed))
		if len(result.failed) > 0 {
			for _, failure := range result.failed {
				fmt.Printf("  %s\n", failure)
			}
			return fmt.Errorf("raw sync completed partially; rerun after resolving %d failed item(s)", len(result.failed))
		}
		return nil
	})
}

func reserveRawSyncPossibleUsage(count int, total int64, exists bool, previousSize, candidateSize int64) (int, int64) {
	if exists {
		if candidateSize > previousSize {
			total += candidateSize - previousSize
		}
		return count, total
	}
	return count + 1, total + candidateSize
}

func newSyncDataAPIClient(rawOrigin string, authClient *cliAPIClient, binding vaultBindingConfig) (*syncDataAPIClient, error) {
	origin, err := normalizeAuthOrigin(rawOrigin)
	if err != nil {
		return nil, errors.New("Auth service returned an invalid sync service origin")
	}
	return &syncDataAPIClient{
		origin: origin, authClient: authClient, binding: binding,
		httpClient: &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

func (c *syncDataAPIClient) request(ctx context.Context, method, route string, bodyFactory func() (io.ReadCloser, error), headers http.Header) (*syncAPIResponse, error) {
	if _, err := syncAPIURL(c.origin, route); err != nil {
		return nil, errors.New("invalid sync data API path")
	}
	for attempt := 0; attempt < 2; attempt++ {
		var body io.ReadCloser
		var err error
		if bodyFactory != nil {
			body, err = bodyFactory()
			if err != nil {
				return nil, err
			}
		}
		requestURL, _ := syncAPIURL(c.origin, route)
		request, err := http.NewRequestWithContext(ctx, method, requestURL, body)
		if err != nil {
			if body != nil {
				_ = body.Close()
			}
			return nil, errors.New("could not create sync data request")
		}
		request.Header.Set("Accept", "application/json")
		if c.authClient == nil || c.authClient.credentials == nil || strings.TrimSpace(c.authClient.credentials.AccessToken) == "" {
			if body != nil {
				_ = body.Close()
			}
			return nil, errCLIReauthenticationRequired
		}
		request.Header.Set("Authorization", "Bearer "+c.authClient.credentials.AccessToken)
		request.Header.Set("X-Project-ID", c.binding.ProjectID)
		request.Header.Set("X-Wiki-ID", c.binding.WikiID)
		request.Header.Set("X-Sync-Binding-ID", c.binding.BindingID)
		for key, values := range headers {
			for _, value := range values {
				request.Header.Add(key, value)
			}
		}
		response, err := c.httpClient.Do(request)
		if err != nil {
			if method == http.MethodPut {
				return nil, errAmbiguousRawTransfer
			}
			return nil, errors.New("could not reach the configured sync data service")
		}
		if response.StatusCode == http.StatusUnauthorized && attempt == 0 && c.authClient.credentials.RefreshToken != "" {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxCLIResponseBytes))
			_ = response.Body.Close()
			if err := c.authClient.refresh(ctx); err != nil {
				return nil, err
			}
			continue
		}
		return &syncAPIResponse{status: response.StatusCode, header: response.Header.Clone(), body: response.Body}, nil
	}
	return nil, errCLIReauthenticationRequired
}

func (c *syncDataAPIClient) listAll(ctx context.Context) (map[string]store.RawSyncFile, error) {
	result := make(map[string]store.RawSyncFile)
	seenTokens := make(map[string]struct{})
	snapshot := ""
	token := ""
	totalFiles := -1
	var total int64
	for pageNumber := 0; pageNumber <= store.MaxRawSyncFiles/cliRawPageSize; pageNumber++ {
		query := url.Values{}
		if token != "" {
			query.Set("page_token", token)
		}
		route := cliRawListRoute
		if len(query) > 0 {
			route += "?" + query.Encode()
		}
		response, err := c.request(ctx, http.MethodGet, route, nil, nil)
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(io.LimitReader(response.body, maxCLIResponseBytes+1))
		closeErr := response.body.Close()
		if readErr != nil || closeErr != nil || len(data) > maxCLIResponseBytes {
			return nil, errors.New("sync service returned an incomplete listing page")
		}
		if response.status < 200 || response.status >= 300 {
			return nil, syncDataStatusError{status: response.status}
		}
		var page remoteRawPage
		if err := json.Unmarshal(data, &page); err != nil || len(page.Files) > cliRawPageSize || len(page.Snapshot) != 64 || page.TotalFiles < 0 || page.TotalFiles > store.MaxRawSyncFiles {
			return nil, errors.New("sync service returned an invalid listing page")
		}
		if totalFiles < 0 {
			totalFiles = page.TotalFiles
		} else if totalFiles != page.TotalFiles {
			return nil, errors.New("remote listing page count changed; retry the command")
		}
		if _, err := hex.DecodeString(page.Snapshot); err != nil {
			return nil, errors.New("sync service returned an invalid listing snapshot")
		}
		if snapshot == "" {
			snapshot = page.Snapshot
		} else if snapshot != page.Snapshot {
			return nil, errors.New("remote listing changed between pages; retry the command")
		}
		for _, file := range page.Files {
			if store.ValidateRawSyncPath(file.Path) != nil || file.Size < 0 || file.Size > store.MaxRawSyncFileBytes || !store.ValidRawSyncSHA256(file.SHA256) || strings.TrimSpace(file.Generation) == "" {
				return nil, errors.New("sync service returned an unsupported raw file")
			}
			if _, duplicate := result[file.Path]; duplicate {
				return nil, errors.New("remote listing repeated a raw path; retry the command")
			}
			if len(result) >= store.MaxRawSyncFiles || total > store.MaxRawSyncTotalBytes-file.Size {
				return nil, errors.New("remote raw tree exceeds sync limits")
			}
			result[file.Path] = file
			total += file.Size
		}
		if page.NextPageToken == "" {
			if len(result) != totalFiles {
				return nil, errors.New("remote listing omitted pages; retry the command")
			}
			return result, nil
		}
		if _, duplicate := seenTokens[page.NextPageToken]; duplicate || page.NextPageToken == token {
			return nil, errors.New("sync service repeated a page token")
		}
		seenTokens[page.NextPageToken] = struct{}{}
		token = page.NextPageToken
	}
	return nil, errors.New("remote listing exceeded the page limit")
}

func (c *syncDataAPIClient) uploadFile(ctx context.Context, inventory *localRawInventory, file localRawFile) error {
	query := url.Values{"path": []string{file.Path}}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/octet-stream")
	headers.Set("X-Content-SHA256", file.SHA256)
	if file.Generation != "" {
		headers.Set("X-Expected-Generation", file.Generation)
	}
	bodyFactory := func() (io.ReadCloser, error) { return openVerifiedLocalRawFile(inventory, file) }
	response, err := c.request(ctx, http.MethodPut, cliRawFileRoute+"?"+query.Encode(), bodyFactory, headers)
	if err != nil {
		return err
	}
	defer response.body.Close()
	if response.status < 200 || response.status >= 300 {
		if response.status >= 500 || response.status == http.StatusRequestTimeout {
			return errAmbiguousRawTransfer
		}
		return syncDataStatusError{status: response.status}
	}
	data, err := io.ReadAll(io.LimitReader(response.body, maxCLIResponseBytes+1))
	if err != nil || len(data) > maxCLIResponseBytes {
		return errAmbiguousRawTransfer
	}
	var result struct {
		SHA256     string `json:"sha256"`
		Generation string `json:"generation"`
	}
	if json.Unmarshal(data, &result) != nil || result.SHA256 != file.SHA256 || strings.TrimSpace(result.Generation) == "" {
		return errAmbiguousRawTransfer
	}
	return nil
}

func (c *syncDataAPIClient) downloadFile(ctx context.Context, vault string, remote store.RawSyncFile) error {
	query := url.Values{"path": []string{remote.Path}, "generation": []string{remote.Generation}}
	response, err := c.request(ctx, http.MethodGet, cliRawFileRoute+"?"+query.Encode(), nil, nil)
	if err != nil {
		return err
	}
	defer response.body.Close()
	if response.status < 200 || response.status >= 300 {
		return syncDataStatusError{status: response.status}
	}
	if response.header.Get("X-Raw-Generation") != remote.Generation {
		return errors.New("sync service returned a different raw generation")
	}
	length := response.header.Get("Content-Length")
	parsed, err := strconv.ParseInt(length, 10, 64)
	if err != nil || parsed != remote.Size {
		return errors.New("sync service returned an unexpected raw file size")
	}
	target, err := safeRawTarget(vault, remote.Path, true)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(target), ".lwc-sync-download-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	hasher := sha256.New()
	count, copyErr := io.Copy(io.MultiWriter(temp, hasher), io.LimitReader(response.body, remote.Size+1))
	if copyErr == nil && (count != remote.Size || hex.EncodeToString(hasher.Sum(nil)) != remote.SHA256) {
		copyErr = errors.New("downloaded raw file failed size or digest verification")
	}
	if copyErr == nil {
		copyErr = temp.Sync()
	}
	closeErr := temp.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if currentTarget, err := safeRawTarget(vault, remote.Path, false); err != nil || currentTarget != target {
		return errors.New("local raw path changed during download; existing files were preserved")
	}
	if err := os.Link(tempPath, target); err != nil {
		if errors.Is(err, os.ErrExist) {
			return errors.New("local raw path appeared after inventory; existing file was preserved")
		}
		return err
	}
	return syncDirectory(filepath.Dir(target))
}

func syncAPIURL(origin, route string) (string, error) {
	if !(route == cliRawListRoute || strings.HasPrefix(route, cliRawListRoute+"?") || route == cliRawFileRoute || strings.HasPrefix(route, cliRawFileRoute+"?")) {
		return "", errors.New("invalid sync API route")
	}
	u, err := url.ParseRequestURI(route)
	if err != nil || u.IsAbs() || u.Host != "" || strings.Contains(route, "\\") || (u.Path != cliRawListRoute && u.Path != cliRawFileRoute) {
		return "", errors.New("invalid sync API route")
	}
	return origin + route, nil
}

func scanLocalRawFiles(ctx context.Context, vault string) (*localRawInventory, error) {
	vault, err := filepath.Abs(strings.TrimSpace(vault))
	if err != nil {
		return nil, errors.New("invalid vault path")
	}
	vaultInfo, err := os.Lstat(vault)
	if err != nil || vaultInfo.Mode()&os.ModeSymlink != 0 || !vaultInfo.IsDir() {
		return nil, errors.New("vault must be an existing real directory")
	}
	rawDir := filepath.Join(vault, "raw")
	pathInfo, err := os.Lstat(rawDir)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.IsDir() {
		return nil, errors.New("vault raw/ must be an existing real directory; run init first")
	}
	root, err := os.OpenRoot(rawDir)
	if err != nil {
		return nil, errors.New("could not securely open vault raw/")
	}
	rootInfo, err := root.Stat(".")
	if err != nil || !os.SameFile(pathInfo, rootInfo) {
		_ = root.Close()
		return nil, errors.New("vault raw/ changed while it was opened")
	}
	inventory := &localRawInventory{RootPath: rawDir, Root: root, RootInfo: rootInfo, Files: make([]localRawFile, 0)}
	if err := scanLocalRawDirectory(ctx, inventory, "."); err != nil {
		_ = inventory.Close()
		return nil, err
	}
	sort.Slice(inventory.Files, func(i, j int) bool { return inventory.Files[i].Path < inventory.Files[j].Path })
	return inventory, nil
}

func scanLocalRawDirectory(ctx context.Context, inventory *localRawInventory, directory string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	openedDirectory, err := inventory.Root.Open(filepath.FromSlash(directory))
	if err != nil {
		return err
	}
	directoryInfo, statErr := openedDirectory.Stat()
	pathInfo, pathErr := inventory.Root.Lstat(filepath.FromSlash(directory))
	if statErr != nil || pathErr != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.IsDir() || !os.SameFile(directoryInfo, pathInfo) {
		_ = openedDirectory.Close()
		return errors.New("raw directory changed during inventory; rerun the command")
	}
	entries, err := openedDirectory.ReadDir(-1)
	closeErr := openedDirectory.Close()
	if err != nil || closeErr != nil {
		return errors.New("could not read raw directory")
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		rel := entry.Name()
		if directory != "." {
			rel = directory + "/" + entry.Name()
		}
		if err := store.ValidateRawSyncPath(rel); err != nil {
			return err
		}
		info, err := inventory.Root.Lstat(filepath.FromSlash(rel))
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("raw tree contains a symlink or changed path: %s", entry.Name())
		}
		if info.IsDir() {
			child, err := inventory.Root.OpenRoot(filepath.FromSlash(rel))
			if err != nil {
				return errors.New("raw directory changed during inventory; rerun the command")
			}
			childInfo, statErr := child.Stat(".")
			closeErr := child.Close()
			if statErr != nil || closeErr != nil || !os.SameFile(info, childInfo) {
				return errors.New("raw directory changed during inventory; rerun the command")
			}
			if err := scanLocalRawDirectory(ctx, inventory, rel); err != nil {
				return err
			}
			continue
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("raw tree contains a symlink or unsupported file: %s", entry.Name())
		}
		if len(inventory.Files) >= store.MaxRawSyncFiles {
			return errors.New("raw tree exceeds the file-count limit")
		}
		file, sourceInfo, err := openLocalRawSource(inventory, rel, nil)
		if err != nil {
			return errors.New("raw file changed during inventory; rerun the command")
		}
		hasher := sha256.New()
		count, copyErr := io.Copy(hasher, io.LimitReader(file, store.MaxRawSyncFileBytes+1))
		after, statErr := file.Stat()
		pathInfo, pathErr := inventory.Root.Lstat(filepath.FromSlash(rel))
		closeErr := file.Close()
		if copyErr != nil || statErr != nil || pathErr != nil || closeErr != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(sourceInfo, after) || !os.SameFile(sourceInfo, pathInfo) || sourceInfo.Size() != after.Size() || !sourceInfo.ModTime().Equal(after.ModTime()) || count != after.Size() {
			return errors.New("raw file changed during inventory; rerun the command")
		}
		if count > store.MaxRawSyncFileBytes {
			return errors.New("raw tree exceeds sync size limits")
		}
		if inventory.Total > store.MaxRawSyncTotalBytes-count {
			return errors.New("raw tree exceeds sync size limits")
		}
		inventory.Files = append(inventory.Files, localRawFile{
			RawSyncFile: store.RawSyncFile{Path: rel, Size: count, SHA256: hex.EncodeToString(hasher.Sum(nil))},
			SourceInfo:  sourceInfo,
		})
		inventory.Total += count
	}
	return nil
}

func openLocalRawSource(inventory *localRawInventory, rel string, expected os.FileInfo) (*os.File, os.FileInfo, error) {
	if inventory == nil || inventory.Root == nil || store.ValidateRawSyncPath(rel) != nil {
		return nil, nil, errors.New("invalid raw source")
	}
	if err := validateLocalRawRoot(inventory); err != nil {
		return nil, nil, err
	}
	if err := validateLocalRawParents(inventory.Root, rel); err != nil {
		return nil, nil, err
	}
	pathInfo, err := inventory.Root.Lstat(filepath.FromSlash(rel))
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() {
		return nil, nil, errors.New("raw source is no longer a regular file")
	}
	opened, err := inventory.Root.Open(filepath.FromSlash(rel))
	if err != nil {
		return nil, nil, errors.New("raw source could not be opened inside raw/")
	}
	info, err := opened.Stat()
	pathInfo, pathErr := inventory.Root.Lstat(filepath.FromSlash(rel))
	parentsErr := validateLocalRawParents(inventory.Root, rel)
	rootErr := validateLocalRawRoot(inventory)
	if err != nil || pathErr != nil || parentsErr != nil || rootErr != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || !pathInfo.Mode().IsRegular() || !os.SameFile(info, pathInfo) || (expected != nil && !os.SameFile(info, expected)) {
		_ = opened.Close()
		return nil, nil, errors.New("raw source changed while it was opened")
	}
	return opened, info, nil
}

func validateLocalRawParents(root *os.Root, rel string) error {
	parts := strings.Split(rel, "/")
	current := ""
	for _, part := range parts[:len(parts)-1] {
		if current == "" {
			current = part
		} else {
			current += "/" + part
		}
		info, err := root.Lstat(filepath.FromSlash(current))
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("raw parent is not a real directory")
		}
		dir, err := root.OpenRoot(filepath.FromSlash(current))
		if err != nil {
			return errors.New("raw parent changed while it was opened")
		}
		openedInfo, statErr := dir.Stat(".")
		closeErr := dir.Close()
		if statErr != nil || closeErr != nil || !os.SameFile(info, openedInfo) {
			return errors.New("raw parent changed while it was opened")
		}
	}
	return nil
}

func validateLocalRawRoot(inventory *localRawInventory) error {
	openedInfo, err := inventory.Root.Stat(".")
	if err != nil || !os.SameFile(openedInfo, inventory.RootInfo) {
		return errors.New("raw root handle changed")
	}
	pathInfo, err := os.Lstat(inventory.RootPath)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.IsDir() || !os.SameFile(pathInfo, inventory.RootInfo) {
		return errors.New("vault raw/ changed during sync; rerun the command")
	}
	return nil
}

func verifyOpenLocalRawFile(inventory *localRawInventory, file localRawFile, opened *os.File, before os.FileInfo) error {
	hasher := sha256.New()
	count, copyErr := io.Copy(hasher, io.LimitReader(opened, store.MaxRawSyncFileBytes+1))
	after, statErr := opened.Stat()
	pathInfo, pathErr := inventory.Root.Lstat(filepath.FromSlash(file.Path))
	parentsErr := validateLocalRawParents(inventory.Root, file.Path)
	rootErr := validateLocalRawRoot(inventory)
	if copyErr != nil || statErr != nil || pathErr != nil || parentsErr != nil || rootErr != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(before, after) || !os.SameFile(before, pathInfo) || (file.SourceInfo != nil && !os.SameFile(before, file.SourceInfo)) || before.Size() != file.Size || after.Size() != file.Size || count != file.Size || hex.EncodeToString(hasher.Sum(nil)) != file.SHA256 || !before.ModTime().Equal(after.ModTime()) {
		return errors.New("raw file changed since inventory")
	}
	return nil
}

func verifyLocalRawFile(inventory *localRawInventory, file localRawFile) error {
	opened, before, err := openLocalRawSource(inventory, file.Path, file.SourceInfo)
	if err != nil {
		return err
	}
	defer opened.Close()
	return verifyOpenLocalRawFile(inventory, file, opened, before)
}

func openVerifiedLocalRawFile(inventory *localRawInventory, file localRawFile) (*os.File, error) {
	opened, before, err := openLocalRawSource(inventory, file.Path, file.SourceInfo)
	if err != nil {
		return nil, err
	}
	if err := verifyOpenLocalRawFile(inventory, file, opened, before); err != nil {
		_ = opened.Close()
		return nil, err
	}
	if _, err := opened.Seek(0, io.SeekStart); err != nil {
		_ = opened.Close()
		return nil, errors.New("raw source could not be rewound")
	}
	return opened, nil
}

func sameLocalRawInventory(before, after *localRawInventory) bool {
	if before == nil || after == nil || !os.SameFile(before.RootInfo, after.RootInfo) || len(before.Files) != len(after.Files) {
		return false
	}
	for i := range before.Files {
		if before.Files[i].Path != after.Files[i].Path || before.Files[i].Size != after.Files[i].Size || before.Files[i].SHA256 != after.Files[i].SHA256 {
			return false
		}
	}
	return true
}

func validateRawSyncUnion(local []localRawFile, remote map[string]store.RawSyncFile) error {
	count, total, err := rawSyncRemoteUsage(remote)
	if err != nil {
		return err
	}
	for _, file := range local {
		if file.Size < 0 || file.Size > store.MaxRawSyncFileBytes {
			return errors.New("local raw tree exceeds sync limits")
		}
		if current, exists := remote[file.Path]; exists {
			total -= current.Size
		} else {
			count++
		}
		if total > store.MaxRawSyncTotalBytes-file.Size {
			return errors.New("combined local and remote raw tree exceeds the sync size limit; no files were uploaded")
		}
		total += file.Size
		if count > store.MaxRawSyncFiles {
			return errors.New("combined local and remote raw tree exceeds the sync file-count limit; no files were uploaded")
		}
	}
	return nil
}

func rawSyncRemoteUsage(remote map[string]store.RawSyncFile) (int, int64, error) {
	count := len(remote)
	if count > store.MaxRawSyncFiles {
		return 0, 0, errors.New("remote raw tree exceeds sync limits")
	}
	var total int64
	for _, file := range remote {
		if file.Size < 0 || file.Size > store.MaxRawSyncFileBytes || total > store.MaxRawSyncTotalBytes-file.Size {
			return 0, 0, errors.New("remote raw tree exceeds sync limits")
		}
		total += file.Size
	}
	return count, total, nil
}

func safeRawTarget(vault, rawRelative string, createParents bool) (string, error) {
	if err := store.ValidateRawSyncPath(rawRelative); err != nil {
		return "", err
	}
	vault, err := filepath.Abs(strings.TrimSpace(vault))
	if err != nil {
		return "", err
	}
	rawDir := filepath.Join(vault, "raw")
	if info, err := os.Lstat(rawDir); err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("vault raw/ is not a real directory")
	}
	parts := strings.Split(filepath.FromSlash(rawRelative), string(filepath.Separator))
	current := rawDir
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) && createParents {
			if err := os.Mkdir(current, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return "", err
			}
			info, err = os.Lstat(current)
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", errors.New("local raw parent path is not a real directory")
		}
	}
	target := filepath.Join(rawDir, filepath.FromSlash(rawRelative))
	if _, err := os.Lstat(target); err == nil {
		return "", errors.New("local raw path already exists; existing file was preserved")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return target, nil
}

var errAmbiguousRawTransfer = errors.New("raw upload response was ambiguous")

func isAmbiguousTransfer(err error) bool { return errors.Is(err, errAmbiguousRawTransfer) }
