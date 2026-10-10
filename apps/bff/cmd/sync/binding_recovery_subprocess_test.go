package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type bindingRecoveryFixture struct {
	mu        sync.Mutex
	bindings  []authSyncBinding
	status    int
	body      []byte
	afterList func()
	requests  [][2]string
}

type bindingRecoveryCLI struct {
	binary     string
	host       string
	configRoot string
	vault      string
	fixture    *bindingRecoveryFixture
	server     *httptest.Server
}

func buildBindingRecoveryCLI(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "lwc-sync")
	command := exec.Command("go", "build", "-o", binary, ".")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	return binary
}

func newBindingRecoveryCLI(t *testing.T, binary string, local *vaultBindingConfig, remote []authSyncBinding) *bindingRecoveryCLI {
	t.Helper()
	fixture := &bindingRecoveryFixture{bindings: append([]authSyncBinding(nil), remote...), status: http.StatusOK}
	server := httptest.NewServer(http.HandlerFunc(fixture.serveHTTP))
	configRoot := filepath.Join(t.TempDir(), "config")
	storeDir := filepath.Join(configRoot, localAuthDirectoryName)
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestJSON(t, filepath.Join(storeDir, localConfigFileName), cliLocalConfig{AuthHost: server.URL})
	writeTestJSON(t, filepath.Join(storeDir, localCredentialsName), cliLocalCredentials{
		AuthHost: server.URL, AccessToken: "fixture-access", RefreshToken: "fixture-refresh",
		SessionID: "fixture-session", UserID: "fixture-user", Role: "user", AuthVersion: 1,
	})
	vault := t.TempDir()
	if local != nil {
		binding := *local
		if binding.Host == "" {
			binding.Host = server.URL
		}
		writeTestJSON(t, vaultBindingPath(vault), binding)
	}
	t.Cleanup(server.Close)
	return &bindingRecoveryCLI{binary: binary, host: server.URL, configRoot: configRoot, vault: vault, fixture: fixture, server: server}
}

func (f *bindingRecoveryFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, [2]string{r.Method, r.URL.Path})
	if r.Header.Get("Authorization") != "Bearer fixture-access" {
		f.mu.Unlock()
		http.Error(w, "unexpected fixture authorization", http.StatusUnauthorized)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/api/v1/auth/cli/bindings" {
		status := f.status
		if status == 0 {
			status = http.StatusOK
		}
		bindings := append([]authSyncBinding(nil), f.bindings...)
		body := append([]byte(nil), f.body...)
		afterList := f.afterList
		f.afterList = nil
		f.mu.Unlock()
		if afterList != nil {
			afterList()
		}
		if status != http.StatusOK {
			http.Error(w, "injected binding-list failure", status)
			return
		}
		if body != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write(body)
			return
		}
		writeTestJSONResponse(w, status, map[string]any{"bindings": bindings})
		return
	}
	if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/auth/cli/bindings/") && strings.HasSuffix(r.URL.Path, "/reauthorize") {
		var input struct {
			BindingID string `json:"binding_id"`
			WikiID    string `json:"wiki_id"`
			Host      string `json:"host"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			f.mu.Unlock()
			http.Error(w, "invalid reauthorize body", http.StatusBadRequest)
			return
		}
		for i := range f.bindings {
			if f.bindings[i].ID == input.BindingID && f.bindings[i].WikiID == input.WikiID && f.bindings[i].Host == input.Host {
				f.bindings[i].ID = "binding-replacement"
				f.bindings[i].Status = "active"
				f.mu.Unlock()
				writeTestJSONResponse(w, http.StatusOK, map[string]string{"binding_id": "binding-replacement"})
				return
			}
		}
		f.mu.Unlock()
		http.Error(w, "binding precondition failed", http.StatusConflict)
		return
	}
	if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/auth/cli/bindings/") && strings.HasSuffix(r.URL.Path, "/revoke") {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) != 8 {
			f.mu.Unlock()
			http.Error(w, "invalid revoke route", http.StatusBadRequest)
			return
		}
		projectID, bindingID := parts[5], parts[6]
		for i := range f.bindings {
			if f.bindings[i].ProjectID == projectID && f.bindings[i].ID == bindingID {
				f.bindings[i].Status = "revoked"
			}
		}
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	f.mu.Unlock()
	http.NotFound(w, r)
}

func (f *bindingRecoveryFixture) setResponse(status int, bindings []authSyncBinding, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = status
	f.bindings = append([]authSyncBinding(nil), bindings...)
	f.body = append([]byte(nil), body...)
}

func (f *bindingRecoveryFixture) setBindingHosts(host string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.bindings {
		if f.bindings[i].Host == "" {
			f.bindings[i].Host = host
		}
	}
}

func (f *bindingRecoveryFixture) setStatus(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = status
}

func (f *bindingRecoveryFixture) setAfterList(afterList func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.afterList = afterList
}

func (f *bindingRecoveryFixture) resetRequests() {
	f.mu.Lock()
	f.requests = nil
	f.mu.Unlock()
}

func (f *bindingRecoveryFixture) requestSnapshot() [][2]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][2]string(nil), f.requests...)
}

func (c *bindingRecoveryCLI) run(args ...string) (string, error) {
	command := exec.Command(c.binary, args...)
	command.Env = append(os.Environ(), "XDG_CONFIG_HOME="+c.configRoot, "LWC_SYNC_AUTH_HOST="+c.host)
	output, err := command.CombinedOutput()
	return string(output), err
}

func (c *bindingRecoveryCLI) recoverArgs(bindingID string, confirm bool) []string {
	args := []string{"binding", "recover", "--vault", c.vault, "--host", c.host, "--project-id", "project-1", "--binding-id", bindingID}
	if confirm {
		args = append(args, "--confirm-same-vault")
	}
	return args
}

func (c *bindingRecoveryCLI) assertRecoveryDidNotMutateServerOrRaw(t *testing.T) {
	t.Helper()
	for _, request := range c.fixture.requestSnapshot() {
		if request[0] != http.MethodGet || request[1] != "/api/v1/auth/cli/bindings" {
			t.Fatalf("recover made a disallowed server request: %v", request)
		}
	}
}

func TestRawSyncCLIRealSubprocessBindingRecover(t *testing.T) {
	binary := buildBindingRecoveryCLI(t)
	newServerBinding := func(host, id, wikiID, projectID, status string) authSyncBinding {
		return authSyncBinding{ID: id, Host: host, WikiID: wikiID, ProjectID: projectID, Status: status}
	}

	for _, scenario := range []struct {
		name    string
		pending bool
	}{
		{name: "missing metadata recovers existing identity"},
		{name: "old init pending metadata recovers and remains idempotent", pending: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			remote := newServerBinding("", "binding-original", "wiki-original", "project-1", "active")
			cli := newBindingRecoveryCLI(t, binary, nil, nil)
			remote.Host = cli.host
			cli.fixture.setResponse(http.StatusOK, []authSyncBinding{remote}, nil)
			rawPath := filepath.Join(cli.vault, "raw", "nested", "attachment.bin")
			if err := os.MkdirAll(filepath.Dir(rawPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(rawPath, []byte("must remain local"), 0o644); err != nil {
				t.Fatal(err)
			}
			if scenario.pending {
				out, err := cli.run("init", "--vault", cli.vault, "--host", cli.host, "--project-id", "project-1")
				if err == nil || !strings.Contains(out, "binding list") || !strings.Contains(out, "binding recover") {
					t.Fatalf("init pending guidance error=%v output=%s", err, out)
				}
				pending, err := loadVaultBinding(cli.vault)
				if err != nil || pending.BindingID != "" || pending.WikiID == remote.WikiID {
					t.Fatalf("init pending metadata=%#v error=%v", pending, err)
				}
			}
			out, err := cli.run("binding", "list", "--host", cli.host, "--json")
			if err != nil || !strings.Contains(out, "binding-original") {
				t.Fatalf("list active binding error=%v output=%s", err, out)
			}
			cli.fixture.resetRequests()
			recoverArgs := cli.recoverArgs("binding-original", true)
			recoverArgs[5] = cli.host + "/" // A normalized host is the same pinned origin.
			out, err = cli.run(recoverArgs...)
			if err != nil {
				t.Fatalf("recover existing binding: %v\n%s", err, out)
			}
			recovered, err := loadVaultBinding(cli.vault)
			if err != nil || recovered != (vaultBindingConfig{Host: cli.host, WikiID: remote.WikiID, ProjectID: remote.ProjectID, BindingID: remote.ID}) {
				t.Fatalf("recovered metadata=%#v error=%v", recovered, err)
			}
			before, err := os.ReadFile(rawPath)
			if err != nil || string(before) != "must remain local" {
				t.Fatalf("recover changed raw fixture bytes=%q error=%v", before, err)
			}
			out, err = cli.run("init", "--vault", cli.vault, "--host", cli.host, "--project-id", "project-1")
			if err != nil {
				t.Fatalf("init after recovery: %v\n%s", err, out)
			}
			after, err := os.ReadFile(rawPath)
			if err != nil || string(after) != "must remain local" {
				t.Fatalf("repeat init changed raw fixture bytes=%q error=%v", after, err)
			}
			cli.assertRecoveryDidNotMutateServerOrRaw(t)
		})
	}

	t.Run("confirmation is required before local or network access", func(t *testing.T) {
		cli := newBindingRecoveryCLI(t, binary, nil, []authSyncBinding{newServerBinding("", "binding-original", "wiki-original", "project-1", "active")})
		cli.fixture.setBindingHosts(cli.host)
		out, err := cli.run(cli.recoverArgs("binding-original", false)...)
		if err == nil || !strings.Contains(out, "--confirm-same-vault") {
			t.Fatalf("missing confirmation error=%v output=%s", err, out)
		}
		if requests := cli.fixture.requestSnapshot(); len(requests) != 0 {
			t.Fatalf("missing confirmation sent requests: %v", requests)
		}
		if _, err := os.Lstat(vaultBindingPath(cli.vault)); !os.IsNotExist(err) {
			t.Fatalf("missing confirmation wrote metadata: %v", err)
		}
	})

	for _, scenario := range []struct {
		name            string
		local           *vaultBindingConfig
		bindings        []authSyncBinding
		body            []byte
		want            string
		wantReads       int
		metadataSymlink bool
	}{
		{name: "unknown binding id", bindings: []authSyncBinding{newServerBinding("", "binding-other", "wiki-original", "project-1", "active")}, want: "active server binding was not found", wantReads: 1},
		{name: "wrong project", bindings: []authSyncBinding{newServerBinding("", "binding-original", "wiki-original", "project-other", "active")}, want: "active server binding was not found", wantReads: 1},
		{name: "wrong returned host", bindings: []authSyncBinding{{ID: "binding-original", Host: "https://other.example.test", WikiID: "wiki-original", ProjectID: "project-1", Status: "active"}}, want: "different auth/control-plane host", wantReads: 1},
		{name: "revoked binding", bindings: []authSyncBinding{newServerBinding("", "binding-original", "wiki-original", "project-1", "revoked")}, want: "not active", wantReads: 1},
		{name: "ambiguous tuple", bindings: []authSyncBinding{newServerBinding("", "binding-original", "wiki-original", "project-1", "active"), newServerBinding("", "binding-original", "wiki-original", "project-1", "active")}, want: "ambiguous", wantReads: 1},
		{name: "invalid remote wiki id", bindings: []authSyncBinding{newServerBinding("", "binding-original", "bad/wiki", "project-1", "active")}, want: "invalid", wantReads: 1},
		{name: "wrong local host", local: &vaultBindingConfig{Host: "https://other.example.test", WikiID: "wiki-pending", ProjectID: "project-1"}, bindings: []authSyncBinding{newServerBinding("", "binding-original", "wiki-original", "project-1", "active")}, want: "different auth/control-plane host"},
		{name: "wrong local project", local: &vaultBindingConfig{Host: "", WikiID: "wiki-pending", ProjectID: "project-other"}, bindings: []authSyncBinding{newServerBinding("", "binding-original", "wiki-original", "project-1", "active")}, want: "different Project"},
		{name: "complete local binding conflict", local: &vaultBindingConfig{Host: "", WikiID: "wiki-other", ProjectID: "project-1", BindingID: "binding-other"}, bindings: []authSyncBinding{newServerBinding("", "binding-original", "wiki-original", "project-1", "active")}, want: "complete local binding"},
		{name: "complete local wiki conflict", local: &vaultBindingConfig{Host: "", WikiID: "wiki-other", ProjectID: "project-1", BindingID: "binding-original"}, bindings: []authSyncBinding{newServerBinding("", "binding-original", "wiki-original", "project-1", "active")}, want: "different complete identity", wantReads: 1},
		{name: "invalid response shape", body: []byte(`{"bindings":"not-an-array"}`), want: "invalid response", wantReads: 1},
		{name: "malformed local metadata", want: "invalid .lwc-sync.json", wantReads: 0},
		{name: "symlink local metadata", metadataSymlink: true, want: "symlink", wantReads: 0},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			remote := scenario.bindings
			if remote == nil {
				remote = []authSyncBinding{newServerBinding("", "binding-original", "wiki-original", "project-1", "active")}
			}
			cli := newBindingRecoveryCLI(t, binary, scenario.local, remote)
			cli.fixture.setBindingHosts(cli.host)
			if scenario.body != nil {
				cli.fixture.setResponse(http.StatusOK, nil, scenario.body)
			}
			if scenario.name == "malformed local metadata" {
				if err := os.WriteFile(vaultBindingPath(cli.vault), []byte("{"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario.metadataSymlink {
				target := filepath.Join(cli.vault, "target.json")
				if err := os.WriteFile(target, []byte(`{"host":"https://example.test"}`), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, vaultBindingPath(cli.vault)); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(vaultBindingPath(cli.vault))
			out, err := cli.run(cli.recoverArgs("binding-original", true)...)
			if err == nil || !strings.Contains(out, scenario.want) {
				t.Fatalf("recover error=%v output=%s; want %q", err, out, scenario.want)
			}
			if reads := len(cli.fixture.requestSnapshot()); reads != scenario.wantReads {
				t.Fatalf("binding reads=%d want=%d requests=%v", reads, scenario.wantReads, cli.fixture.requestSnapshot())
			}
			if scenario.metadataSymlink {
				info, err := os.Lstat(vaultBindingPath(cli.vault))
				if err != nil || info.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("symlink metadata changed: info=%v err=%v", info, err)
				}
			} else {
				after, err := os.ReadFile(vaultBindingPath(cli.vault))
				if len(before) == 0 && os.IsNotExist(err) {
					return
				}
				if err != nil || string(before) != string(after) {
					t.Fatalf("failed recovery changed metadata: before=%q after=%q err=%v", before, after, err)
				}
			}
			cli.assertRecoveryDidNotMutateServerOrRaw(t)
		})
	}

	t.Run("network and write failures retry without replacing local identity", func(t *testing.T) {
		cli := newBindingRecoveryCLI(t, binary, nil, []authSyncBinding{newServerBinding("", "binding-original", "wiki-original", "project-1", "active")})
		cli.fixture.setBindingHosts(cli.host)
		cli.fixture.setStatus(http.StatusServiceUnavailable)
		out, err := cli.run(cli.recoverArgs("binding-original", true)...)
		if err == nil || !strings.Contains(out, "HTTP 503") {
			t.Fatalf("network failure error=%v output=%s", err, out)
		}
		if _, err := os.Lstat(vaultBindingPath(cli.vault)); !os.IsNotExist(err) {
			t.Fatalf("network failure wrote metadata: %v", err)
		}
		cli.fixture.setStatus(http.StatusOK)
		if out, err := cli.run(cli.recoverArgs("binding-original", true)...); err != nil {
			t.Fatalf("recovery retry after network failure: %v\n%s", err, out)
		}
		cli.assertRecoveryDidNotMutateServerOrRaw(t)

		failed := newBindingRecoveryCLI(t, binary, nil, []authSyncBinding{newServerBinding("", "binding-original", "wiki-original", "project-1", "active")})
		failed.fixture.setBindingHosts(failed.host)
		if err := os.Chmod(failed.vault, 0o700); err != nil {
			t.Fatal(err)
		}
		failed.fixture.setAfterList(func() { _ = os.Chmod(failed.vault, 0o500) })
		out, err = failed.run(failed.recoverArgs("binding-original", true)...)
		_ = os.Chmod(failed.vault, 0o700)
		if err == nil || !strings.Contains(out, "could not restore local binding metadata") {
			t.Fatalf("write failure error=%v output=%s", err, out)
		}
		if _, err := os.Lstat(vaultBindingPath(failed.vault)); !os.IsNotExist(err) {
			t.Fatalf("write failure left binding metadata: %v", err)
		}
		if out, err := failed.run(failed.recoverArgs("binding-original", true)...); err != nil {
			t.Fatalf("recovery retry after write failure: %v\n%s", err, out)
		}
		failed.assertRecoveryDidNotMutateServerOrRaw(t)
	})

	t.Run("concurrent local metadata change is preserved and retryable", func(t *testing.T) {
		cli := newBindingRecoveryCLI(t, binary, nil, []authSyncBinding{newServerBinding("", "binding-original", "wiki-original", "project-1", "active")})
		cli.fixture.setBindingHosts(cli.host)
		concurrent := vaultBindingConfig{Host: cli.host, WikiID: "concurrent-wiki", ProjectID: "project-1"}
		cli.fixture.setAfterList(func() { _ = writeVaultBinding(cli.vault, concurrent) })
		out, err := cli.run(cli.recoverArgs("binding-original", true)...)
		if err == nil || !strings.Contains(out, "while the server request was in progress") {
			t.Fatalf("concurrent change error=%v output=%s", err, out)
		}
		current, err := loadVaultBinding(cli.vault)
		if err != nil || current != concurrent {
			t.Fatalf("concurrent metadata overwritten: current=%#v error=%v", current, err)
		}
		if out, err := cli.run(cli.recoverArgs("binding-original", true)...); err != nil {
			t.Fatalf("recovery retry after concurrent change: %v\n%s", err, out)
		}
		current, err = loadVaultBinding(cli.vault)
		if err != nil || current.WikiID != "wiki-original" || current.BindingID != "binding-original" {
			t.Fatalf("retry did not recover original identity: current=%#v error=%v", current, err)
		}
		cli.assertRecoveryDidNotMutateServerOrRaw(t)
	})

	t.Run("exact complete metadata is idempotent", func(t *testing.T) {
		cli := newBindingRecoveryCLI(t, binary, &vaultBindingConfig{Host: "", WikiID: "wiki-original", ProjectID: "project-1", BindingID: "binding-original"}, []authSyncBinding{newServerBinding("", "binding-original", "wiki-original", "project-1", "active")})
		cli.fixture.setBindingHosts(cli.host)
		before, err := os.ReadFile(vaultBindingPath(cli.vault))
		if err != nil {
			t.Fatal(err)
		}
		out, err := cli.run(cli.recoverArgs("binding-original", true)...)
		if err != nil {
			t.Fatalf("idempotent recover: %v\n%s", err, out)
		}
		after, err := os.ReadFile(vaultBindingPath(cli.vault))
		if err != nil || string(before) != string(after) {
			t.Fatalf("idempotent recover rewrote metadata: before=%s after=%s err=%v", before, after, err)
		}
		cli.assertRecoveryDidNotMutateServerOrRaw(t)
	})

	t.Run("pending reauthorize guidance points to list and recover", func(t *testing.T) {
		for _, missing := range []bool{false, true} {
			cli := newBindingRecoveryCLI(t, binary, &vaultBindingConfig{Host: "", WikiID: "wiki-pending", ProjectID: "project-1"}, nil)
			if missing {
				if err := os.Remove(vaultBindingPath(cli.vault)); err != nil {
					t.Fatal(err)
				}
			}
			out, err := cli.run("binding", "reauthorize", "--vault", cli.vault, "--host", cli.host, "--project-id", "project-1")
			if err == nil || !strings.Contains(out, "binding list") || !strings.Contains(out, "binding recover") {
				t.Fatalf("reauthorize error=%v output=%s", err, out)
			}
			if requests := cli.fixture.requestSnapshot(); len(requests) != 0 {
				t.Fatalf("reauthorize guidance made network calls: %v", requests)
			}
		}
	})

	t.Run("init guidance does not send host or revoked pending identities to impossible recovery", func(t *testing.T) {
		wrongHost := newBindingRecoveryCLI(t, binary, nil, []authSyncBinding{{ID: "binding-original", Host: "https://other.example.test", WikiID: "wiki-original", ProjectID: "project-1", Status: "active"}})
		out, err := wrongHost.run("init", "--vault", wrongHost.vault, "--host", wrongHost.host, "--project-id", "project-1")
		if err == nil || !strings.Contains(out, "different auth/control-plane host") || !strings.Contains(out, "binding list") || strings.Contains(out, "binding recover") {
			t.Fatalf("wrong-host init guidance error=%v output=%s", err, out)
		}
		wrongHost.assertRecoveryDidNotMutateServerOrRaw(t)

		revoked := newBindingRecoveryCLI(t, binary, &vaultBindingConfig{WikiID: "wiki-original", ProjectID: "project-1"}, []authSyncBinding{newServerBinding("", "binding-original", "wiki-original", "project-1", "revoked")})
		revoked.fixture.setBindingHosts(revoked.host)
		out, err = revoked.run("init", "--vault", revoked.vault, "--host", revoked.host, "--project-id", "project-1")
		if err == nil || !strings.Contains(out, "not active") || !strings.Contains(out, "binding list") || !strings.Contains(out, "recovery requires") || strings.Contains(out, "binding reauthorize") {
			t.Fatalf("revoked pending init guidance error=%v output=%s", err, out)
		}
		revoked.assertRecoveryDidNotMutateServerOrRaw(t)
	})

	t.Run("existing explicit reauthorize and revoke remain available", func(t *testing.T) {
		local := &vaultBindingConfig{Host: "", WikiID: "wiki-original", ProjectID: "project-1", BindingID: "binding-original"}
		cli := newBindingRecoveryCLI(t, binary, local, []authSyncBinding{newServerBinding("", "binding-original", "wiki-original", "project-1", "active")})
		cli.fixture.setBindingHosts(cli.host)
		out, err := cli.run("binding", "reauthorize", "--vault", cli.vault, "--host", cli.host, "--project-id", "project-1")
		if err != nil {
			t.Fatalf("explicit reauthorize failed: %v\n%s", err, out)
		}
		binding, err := loadVaultBinding(cli.vault)
		if err != nil || binding.BindingID != "binding-replacement" {
			t.Fatalf("reauthorize metadata=%#v error=%v", binding, err)
		}
		out, err = cli.run("binding", "revoke", "--host", cli.host, "--project-id", "project-1", "--binding-id", "binding-replacement")
		if err != nil {
			t.Fatalf("explicit revoke failed: %v\n%s", err, out)
		}
		requests := cli.fixture.requestSnapshot()
		if len(requests) != 3 || requests[0][0] != http.MethodGet || requests[1][0] != http.MethodPost || requests[2][0] != http.MethodPost {
			t.Fatalf("reauthorize/revoke request sequence=%v", requests)
		}
	})
}
