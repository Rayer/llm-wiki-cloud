package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPrepareI18nTargetAssemblesStablePublicSources(t *testing.T) {
	root := t.TempDir()
	messagesDir := filepath.Join(root, "apps/frontend/src/messages")
	if err := os.MkdirAll(messagesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	base := `{"App":{"connect":"Connect {resourceURL}","embedded":"Text @cac:unknown","instructions":"@cac:mcp.instructions"}}`
	registry := `{"schema_version":1,"base_locale":"en","entries":{"App.connect":{"kind":"literal","params":["resourceURL"]},"App.instructions":{"kind":"cac","ref":"mcp.instructions","source_locale":"en","context":"Shared public MCP guidance."}}}`
	if err := os.WriteFile(filepath.Join(messagesDir, "en.json"), []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(messagesDir, "source-refs.json"), []byte(registry), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(messagesDir, "zh-TW.json"), []byte(`{"App":{"connect":"連線 {resourceURL}","embedded":"文字 @cac:unknown","instructions":"共用指引"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(messagesDir, "seed-provenance.json"), []byte(`{"schema_version":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	projection := i18nProjection{
		SchemaVersion: 1, Environment: "local", Target: "i18n",
		I18n: i18nConfig{
			SchemaVersion: 1, BaseLocale: "en", TargetLocales: []string{"zh_tw", "zh", "en"}, DefaultLocale: "zh_tw",
			LocaleMetadata: []i18nLocale{
				{Token: "zh_tw", UILocale: "zh-TW", IntlLocale: "zh-TW", Label: "繁體中文"},
				{Token: "zh", UILocale: "zh-CN", IntlLocale: "zh-CN", Label: "简体中文"},
				{Token: "en", UILocale: "en", IntlLocale: "en-US", Label: "English"},
			},
			Translation: i18nTranslation{
				Provider: "custom", Protocol: "openai_chat_completions", Endpoint: "https://api.example.test/v1/chat/completions",
				Model: "test-model", RequestTimeoutSeconds: 30, MaxOutputTokens: 512,
				Secret: secretBinding{Source: "environment", Target: "DEEPSEEK_API_KEY", EnvName: "LLM_API_KEY"},
			},
			PromptVersion: "test-prompt-v1",
		},
		MCP: i18nMCPSource{Instructions: "Use only the currently listed tools.", ToolDescriptions: struct {
			QueryProject string `json:"query_project"`
			AddRaw       string `json:"add_raw"`
			ImportURL    string `json:"import_url"`
		}{QueryProject: "Search existing knowledge.", AddRaw: "Save supplied Markdown.", ImportURL: "Import a public URL."}},
	}
	projectionBytes, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "generated")
	sourceRevision := strings.Repeat("a", 40)
	if err := prepareI18nTarget(root, output, projectionBytes, "local", sourceRevision); err != nil {
		t.Fatal(err)
	}
	artifactDir, err := currentI18nDirectory(output)
	if err != nil {
		t.Fatal(err)
	}
	var baseMap map[string]string
	if err := readJSON(filepath.Join(artifactDir, "base.json"), &baseMap); err != nil {
		t.Fatal(err)
	}
	if baseMap["App.instructions"] != projection.MCP.Instructions || baseMap["App.embedded"] != "Text @cac:unknown" {
		t.Fatalf("unexpected source assembly: %#v", baseMap)
	}
	var input i18nTranslationInput
	if err := readJSON(filepath.Join(artifactDir, "translation-input.json"), &input); err != nil {
		t.Fatal(err)
	}
	if input.SourceRevision != sourceRevision || input.CatalogRevision == "" || len(input.Entries) != 3 {
		t.Fatalf("unexpected translation input identity: %#v", input)
	}
	var connect *i18nEntry
	for i := range input.Entries {
		if input.Entries[i].Key == "App.connect" {
			connect = &input.Entries[i]
		}
	}
	if connect == nil || !reflect.DeepEqual(connect.Params, []string{"resourceURL"}) || connect.Origin.Kind != "literal" {
		t.Fatalf("runtime parameters or entry provenance were lost: %#v", connect)
	}
	first := make(map[string][]byte)
	for _, name := range []string{"pipeline.json", "translation-input.json", "base.json", "source-manifest.json"} {
		first[name], err = os.ReadFile(filepath.Join(artifactDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(first[name]), "TEST_ONLY_SECRET") {
			t.Fatalf("secret value leaked into %s", name)
		}
	}
	if err := prepareI18nTarget(root, output, projectionBytes, "local", sourceRevision); err != nil {
		t.Fatal(err)
	}
	for name, expected := range first {
		actual, err := os.ReadFile(filepath.Join(artifactDir, name))
		if err != nil || !reflect.DeepEqual(actual, expected) {
			t.Fatalf("%s changed across an identical assembly: %v", name, err)
		}
	}
	if currentAfterRepeat, err := currentI18nDirectory(output); err != nil || currentAfterRepeat != artifactDir {
		t.Fatalf("identical source did not reuse its immutable snapshot: %v", err)
	}
	if err := prepareI18nTarget(root, output, projectionBytes, "local", strings.Repeat("c", 40)); err != nil {
		t.Fatalf("same content at a later source revision failed: %v", err)
	}
	commitSnapshot, err := currentI18nDirectory(output)
	if err != nil || commitSnapshot == artifactDir {
		t.Fatalf("source revision did not get a distinct snapshot: %v", err)
	}
	var commitConfig i18nBuildConfig
	if err := readJSON(filepath.Join(commitSnapshot, "pipeline.json"), &commitConfig); err != nil ||
		commitConfig.CatalogRevision != input.CatalogRevision || commitConfig.SourceRevision != strings.Repeat("c", 40) {
		t.Fatalf("same content snapshot lost its content identity or source trace: %#v %v", commitConfig, err)
	}
	updatedProjection := projection
	updatedProjection.I18n.TargetLocales = []string{"zh_tw", "en"}
	updatedProjection.I18n.LocaleMetadata = []i18nLocale{
		projection.I18n.LocaleMetadata[0], projection.I18n.LocaleMetadata[2],
	}
	updatedProjection.I18n.Translation.Model = "test-model-next"
	updatedBytes, err := json.Marshal(updatedProjection)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareI18nTarget(root, output, updatedBytes, "local", strings.Repeat("d", 40)); err != nil {
		t.Fatalf("changed target and model failed with unchanged content: %v", err)
	}
	updatedSnapshot, err := currentI18nDirectory(output)
	if err != nil || updatedSnapshot == commitSnapshot {
		t.Fatalf("changed config did not get a distinct immutable snapshot: %v", err)
	}
	var updatedConfig i18nBuildConfig
	if err := readJSON(filepath.Join(updatedSnapshot, "pipeline.json"), &updatedConfig); err != nil ||
		updatedConfig.CatalogRevision != input.CatalogRevision || updatedConfig.SnapshotRevision == commitConfig.SnapshotRevision ||
		updatedConfig.I18n.Translation.Model != "test-model-next" {
		t.Fatalf("changed config snapshot lost its identity: %#v %v", updatedConfig, err)
	}
	for name, expected := range first {
		actual, err := os.ReadFile(filepath.Join(artifactDir, name))
		if err != nil || !reflect.DeepEqual(actual, expected) {
			t.Fatalf("later snapshots changed the original %s artifact: %v", name, err)
		}
	}

	badRegistry := strings.Replace(registry, `"mcp.instructions"`, `"private.secret.path"`, 1)
	if err := os.WriteFile(filepath.Join(messagesDir, "source-refs.json"), []byte(badRegistry), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := prepareI18nTarget(root, output, projectionBytes, "local", sourceRevision); err == nil {
		t.Fatal("undeclared CaC reference unexpectedly assembled")
	}
	currentAfterFailure, err := currentI18nDirectory(output)
	if err != nil || currentAfterFailure != updatedSnapshot {
		t.Fatalf("failed assembly moved the current revision pointer: %v", err)
	}
	actual, err := os.ReadFile(filepath.Join(artifactDir, "base.json"))
	if err != nil || !reflect.DeepEqual(actual, first["base.json"]) {
		t.Fatalf("failed assembly replaced the prior complete artifact: %v", err)
	}
}

func TestPrepareI18nTargetDoesNotReadSecrets(t *testing.T) {
	root, err := filepath.Abs("../../../../")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LWC_REPOSITORY_ROOT", root)
	output := filepath.Join(t.TempDir(), "generated")
	readerCalls := 0
	reader := func(context.Context) (secretReader, error) {
		readerCalls++
		return nil, errors.New("secret reader must not be initialized for i18n assembly")
	}
	if err := runPrepareTargetModeAtSource(context.Background(), "i18n", "local", output, false,
		strings.Repeat("b", 40), reader); err != nil {
		t.Fatal(err)
	}
	if readerCalls != 0 {
		t.Fatalf("i18n assembly initialized a secret reader %d times", readerCalls)
	}
	artifactDir, err := currentI18nDirectory(output)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(artifactDir, "source-manifest.json")); err != nil {
		t.Fatal(err)
	}
}

func readJSON(path string, destination any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, destination)
}
