//go:build darwin || linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/rayer/llm-wiki-bff/internal/wikiindex"
)

type stagedSyntoConfig struct {
	Provider string `json:"provider"`
	URL      string `json:"url"`
	Fast     string `json:"fast"`
	Heavy    string `json:"heavy"`
}

func stagedHasNativeState(files stagedTree) bool {
	for path := range files {
		if strings.HasPrefix(path, ".synto/state.db") {
			return true
		}
	}
	return false
}

type stagedNativeReceipt struct {
	Version        string `json:"synto_version"`
	Schema         string `json:"schema"`
	Status         string `json:"status"`
	Validation     string `json:"validation"`
	Completeness   string `json:"internal_completeness"`
	Implementation string `json:"synto_implementation"`
	Digest         string `json:"native_artifact_digest"`
}

func stagedNativeDigest(files stagedTree) string {
	native := stagedTree{}
	for path, file := range files {
		if path == "synto.toml" || strings.HasPrefix(path, "raw/") || strings.HasPrefix(path, "wiki/") || strings.HasPrefix(path, ".synto/state.db") {
			native[path] = file
		}
	}
	return native.digest()
}

func (s stagedSyntoConfig) validate() error {
	u, err := url.Parse(s.URL)
	model := regexp.MustCompile(`^[A-Za-z0-9._/-]{1,128}$`)
	if err != nil || s.Provider != "custom" || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !model.MatchString(s.Fast) || !model.MatchString(s.Heavy) {
		return errors.New("Synto requires custom provider, credential-free HTTPS endpoint and valid model names")
	}
	return nil
}
func (s stagedSyntoConfig) toml() []byte {
	return []byte(fmt.Sprintf(`[providers.default]
name = "custom"
url = %q
api_key_env = "DEEPSEEK_API_KEY"
timeout = 120
[models.fast]
provider = "default"
model = %q
ctx = 8192
[models.heavy]
provider = "default"
model = %q
ctx = 32768
[pipeline]
auto_commit = false
auto_maintain = false
auto_approve = false
max_concepts_per_source = 4
`, s.URL, s.Fast, s.Heavy))
}

// Acceptance proves usable public producer output, not exhaustive internal success.
func stagedValidateProducer(vault, export string, before stagedTree) error {
	after, err := stagedReadTree(vault)
	if err != nil {
		return err
	}
	raw, original := after.subset("raw/"), before.subset("raw/")
	if len(raw) == 0 || raw.digest() != original.digest() || !bytes.Equal(stagedJSON(raw.mtimes()), stagedJSON(original.mtimes())) {
		return errors.New("native run changed raw source bytes or mtimes")
	}
	files, err := stagedReadTree(export)
	if err != nil {
		return err
	}
	data := files["index/INDEX.json"].Data
	if _, err := wikiindex.DecodeSyntoIdentityPlan(data); err != nil {
		return errors.New("invalid public agents INDEX export")
	}
	// The production decoder validates schema, IDs and safe article paths.
	// Read the original paths here: ByPath includes only entity-bound articles,
	// which the raw public pack does not always provide before worker enrichment.
	var index struct {
		Stats struct {
			FailedNotes    int `json:"failed_note_count"`
			FailedConcepts int `json:"failed_concept_count"`
		} `json:"stats"`
		Articles []struct {
			Path string `json:"path"`
		} `json:"articles"`
	}
	if err := json.Unmarshal(data, &index); err != nil {
		return err
	}
	if index.Stats.FailedNotes > 0 || index.Stats.FailedConcepts > 0 {
		return errors.New("public export reports producer failures")
	}
	usable := 0
	for _, article := range index.Articles {
		if len(bytes.TrimSpace(files[article.Path].Data)) == 0 {
			return errors.New("public export missing or empty referenced article body")
		}
		if !wikiindex.IsSyntoRootPage("wiki/" + filepath.Base(article.Path)) {
			usable++
		}
	}
	if usable == 0 {
		return errors.New("public export contains no usable articles")
	}
	return nil
}

func stagedEnvironment(output string, inference, cloud bool) []string {
	env := []string{"PATH=" + os.Getenv("PATH"), "PYTHONDONTWRITEBYTECODE=1", "HOME=" + filepath.Join(output, "home"), "XDG_CONFIG_HOME=" + filepath.Join(output, "xdg"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	if experimentBuildID != "" {
		env = append(env, "TMPDIR="+filepath.Join(output, "tmp"), "XDG_CACHE_HOME="+filepath.Join(output, "home", ".cache"))
	}
	if inference {
		for _, key := range []string{"DEEPSEEK_API_KEY", "SYNTO_API_KEY"} {
			if value, ok := os.LookupEnv(key); ok {
				env = append(env, key+"="+value)
			}
		}
	}
	if cloud {
		for _, key := range []string{"HOME", "GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_CLOUD_PROJECT"} {
			if value, ok := os.LookupEnv(key); ok {
				env = append(env, key+"="+value)
			}
		}
	}
	return env
}

// Identity covers the resolved executable and public version (checked separately).
// A launcher hash is not a digest of its installed package or dependencies.
func stagedSyntoIdentity(executable string) (string, error) {
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", err
	}
	launcher, err := stagedReadFile(resolved)
	if err != nil {
		return "", err
	}
	return stagedHash(stagedJSON(map[string]string{"resolved_executable": resolved, "sha256": stagedHash(launcher)})), nil
}
