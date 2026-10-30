package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	maxI18nSourceBytes = 4 << 20
	maxI18nEntries     = 5000
	maxI18nEntryBytes  = 64 << 10
	maxI18nKeyBytes    = 200
)

var (
	i18nLocaleToken  = regexp.MustCompile(`^[a-z]{2,3}(?:_[a-z]{2})?$`)
	i18nSourceSHA    = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)
	regexpI18nDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)
	i18nParam        = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)\}`)
	i18nCACMarker    = regexp.MustCompile(`^@cac:([a-z0-9_.-]+)$`)
)

type i18nProjection struct {
	SchemaVersion int           `json:"schema_version"`
	Environment   string        `json:"environment"`
	Target        string        `json:"target"`
	I18n          i18nConfig    `json:"i18n"`
	MCP           i18nMCPSource `json:"mcp"`
}

type i18nConfig struct {
	SchemaVersion  int             `json:"schema_version"`
	BaseLocale     string          `json:"base_locale"`
	TargetLocales  []string        `json:"target_locales"`
	DefaultLocale  string          `json:"default_locale"`
	LocaleMetadata []i18nLocale    `json:"locale_metadata"`
	Translation    i18nTranslation `json:"translation"`
	PromptVersion  string          `json:"prompt_version"`
}

type i18nLocale struct {
	Token      string `json:"token"`
	UILocale   string `json:"ui_locale"`
	IntlLocale string `json:"intl_locale"`
	Label      string `json:"label"`
}

type i18nTranslation struct {
	Provider              string        `json:"provider"`
	Protocol              string        `json:"protocol"`
	Endpoint              string        `json:"endpoint"`
	Model                 string        `json:"model"`
	RequestTimeoutSeconds int           `json:"request_timeout_seconds"`
	MaxOutputTokens       int           `json:"max_output_tokens"`
	Secret                secretBinding `json:"secret"`
}

type i18nMCPSource struct {
	Instructions     string `json:"instructions"`
	ToolDescriptions struct {
		QueryProject string `json:"query_project"`
		AddRaw       string `json:"add_raw"`
		ImportURL    string `json:"import_url"`
	} `json:"tool_descriptions"`
}

type i18nRegistry struct {
	SchemaVersion int                       `json:"schema_version"`
	BaseLocale    string                    `json:"base_locale"`
	Entries       map[string]i18nSourceRule `json:"entries"`
}

type i18nSourceRule struct {
	Kind              string   `json:"kind"`
	Ref               string   `json:"ref,omitempty"`
	SourceLocale      string   `json:"source_locale,omitempty"`
	Context           string   `json:"context,omitempty"`
	Params            []string `json:"params,omitempty"`
	ProtectedLiterals []string `json:"protected_literals,omitempty"`
}

type i18nEntry struct {
	Key               string     `json:"key"`
	Text              string     `json:"text"`
	SourceLocale      string     `json:"source_locale"`
	Context           string     `json:"context"`
	Params            []string   `json:"params"`
	ProtectedLiterals []string   `json:"protected_literals"`
	SourceDigest      string     `json:"source_digest"`
	Origin            i18nOrigin `json:"origin"`
}

type i18nOrigin struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
	Ref  string `json:"ref,omitempty"`
}

type i18nBuildConfig struct {
	SchemaVersion   int        `json:"schema_version"`
	Environment     string     `json:"environment"`
	Target          string     `json:"target"`
	SourceRevision  string     `json:"source_revision"`
	CatalogRevision string     `json:"catalog_revision"`
	I18n            i18nConfig `json:"i18n"`
}

type i18nSourceFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type i18nSourceManifest struct {
	SchemaVersion       int              `json:"schema_version"`
	Environment         string           `json:"environment"`
	SourceRevision      string           `json:"source_revision"`
	CatalogRevision     string           `json:"catalog_revision"`
	ConfigDigest        string           `json:"config_digest"`
	CACProjectionDigest string           `json:"cac_projection_digest"`
	EntryCount          int              `json:"entry_count"`
	KeySetDigest        string           `json:"key_set_digest"`
	Files               []i18nSourceFile `json:"files"`
}

type i18nTranslationInput struct {
	SchemaVersion   int         `json:"schema_version"`
	SourceRevision  string      `json:"source_revision"`
	CatalogRevision string      `json:"catalog_revision"`
	BaseLocale      string      `json:"base_locale"`
	Entries         []i18nEntry `json:"entries"`
}

type i18nDigestEntry struct {
	Key               string   `json:"key"`
	Text              string   `json:"text"`
	SourceLocale      string   `json:"source_locale"`
	Params            []string `json:"params"`
	ProtectedLiterals []string `json:"protected_literals"`
}

type i18nRevisionInput struct {
	SchemaVersion       int         `json:"schema_version"`
	Entries             []i18nEntry `json:"entries"`
	RegistryDigest      string      `json:"registry_digest"`
	CACProjectionDigest string      `json:"cac_projection_digest"`
}

type i18nPublicConfig struct {
	SchemaVersion   int          `json:"schema_version"`
	BaseLocale      string       `json:"base_locale"`
	TargetLocales   []string     `json:"target_locales"`
	DefaultLocale   string       `json:"default_locale"`
	LocaleMetadata  []i18nLocale `json:"locale_metadata"`
	Provider        string       `json:"provider"`
	Protocol        string       `json:"protocol"`
	Endpoint        string       `json:"endpoint"`
	Model           string       `json:"model"`
	TimeoutSeconds  int          `json:"timeout_seconds"`
	MaxOutputTokens int          `json:"max_output_tokens"`
	PromptVersion   string       `json:"prompt_version"`
	SecretSource    string       `json:"secret_source"`
	SecretTarget    string       `json:"secret_target"`
}

func prepareI18nTarget(root, output string, source []byte, environment, sourceSHA string) error {
	var projection i18nProjection
	decoder := json.NewDecoder(bytes.NewReader(source))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&projection); err != nil {
		return errors.New("selected i18n SSOT is malformed")
	}
	if projection.SchemaVersion != 1 || projection.Environment != environment || projection.Target != "i18n" {
		return errors.New("selected i18n SSOT identity is invalid")
	}
	if sourceSHA == "" {
		var err error
		sourceSHA, err = repositorySourceSHA(root)
		if err != nil {
			return fmt.Errorf("resolve i18n source revision: %w", err)
		}
	}
	if !i18nSourceSHA.MatchString(sourceSHA) {
		return errors.New("i18n source revision must be a full Git SHA")
	}
	if err := validateI18nConfig(projection.I18n, environment); err != nil {
		return err
	}
	if err := validateI18NMCPSource(projection.MCP); err != nil {
		return err
	}

	messagesPath := filepath.Join(root, "apps/frontend/src/messages/en.json")
	registryPath := filepath.Join(root, "apps/frontend/src/messages/source-refs.json")
	seedMapPath := filepath.Join(root, "apps/frontend/src/messages/zh-TW.json")
	seedProvenancePath := filepath.Join(root, "apps/frontend/src/messages/seed-provenance.json")
	messagesBytes, err := readI18nSource(messagesPath)
	if err != nil {
		return fmt.Errorf("read i18n base source: %w", err)
	}
	registryBytes, err := readI18nSource(registryPath)
	if err != nil {
		return fmt.Errorf("read i18n source registry: %w", err)
	}
	seedMapBytes, err := readI18nSource(seedMapPath)
	if err != nil {
		return fmt.Errorf("read Traditional Chinese translation seed: %w", err)
	}
	seedProvenanceBytes, err := readI18nSource(seedProvenancePath)
	if err != nil {
		return fmt.Errorf("read i18n seed provenance: %w", err)
	}
	baseValue, err := decodeI18nUniqueJSON(messagesBytes)
	if err != nil {
		return errors.New("i18n base source is malformed or contains duplicate keys")
	}
	if _, err := decodeI18nUniqueJSON(registryBytes); err != nil {
		return errors.New("i18n source registry is malformed or contains duplicate keys")
	}
	var registry i18nRegistry
	registryDecoder := json.NewDecoder(bytes.NewReader(registryBytes))
	registryDecoder.DisallowUnknownFields()
	if err := registryDecoder.Decode(&registry); err != nil {
		return errors.New("i18n source registry has an invalid shape")
	}
	if registry.SchemaVersion != 1 || registry.BaseLocale != projection.I18n.BaseLocale || registry.Entries == nil {
		return errors.New("i18n source registry identity is invalid")
	}

	baseTree, ok := baseValue.(map[string]any)
	if !ok {
		return errors.New("i18n base source must be a JSON object")
	}
	flat := make(map[string]string)
	if err := flattenI18nSource(baseTree, "", flat); err != nil {
		return err
	}
	if len(flat) == 0 || len(flat) > maxI18nEntries {
		return errors.New("i18n base source entry count is invalid")
	}
	entries, base, err := assembleI18nEntries(flat, registry, projection.MCP, projection.I18n)
	if err != nil {
		return err
	}

	cacBytes, err := json.Marshal(projection.MCP)
	if err != nil {
		return errors.New("i18n CaC projection cannot be encoded")
	}
	cacDigest := digestI18n(cacBytes)
	registryDigest := digestI18n(registryBytes)
	revisionBytes, err := json.Marshal(i18nRevisionInput{
		SchemaVersion:       1,
		Entries:             entries,
		RegistryDigest:      registryDigest,
		CACProjectionDigest: cacDigest,
	})
	if err != nil {
		return errors.New("i18n catalog identity cannot be encoded")
	}
	catalogRevision := digestI18n(revisionBytes)
	publicConfig := i18nPublicConfig{
		SchemaVersion: projection.I18n.SchemaVersion, BaseLocale: projection.I18n.BaseLocale,
		TargetLocales: projection.I18n.TargetLocales, DefaultLocale: projection.I18n.DefaultLocale,
		LocaleMetadata: projection.I18n.LocaleMetadata, Provider: projection.I18n.Translation.Provider,
		Protocol: projection.I18n.Translation.Protocol, Endpoint: projection.I18n.Translation.Endpoint,
		Model:           projection.I18n.Translation.Model,
		TimeoutSeconds:  projection.I18n.Translation.RequestTimeoutSeconds,
		MaxOutputTokens: projection.I18n.Translation.MaxOutputTokens,
		PromptVersion:   projection.I18n.PromptVersion,
		SecretSource:    projection.I18n.Translation.Secret.Source,
		SecretTarget:    projection.I18n.Translation.Secret.Target,
	}
	configBytes, err := json.Marshal(publicConfig)
	if err != nil {
		return errors.New("i18n public config cannot be encoded")
	}
	keys := make([]string, 0, len(entries))
	for _, entry := range entries {
		keys = append(keys, entry.Key)
	}
	keySetBytes, err := json.Marshal(keys)
	if err != nil {
		return errors.New("i18n key set cannot be encoded")
	}
	manifest := i18nSourceManifest{
		SchemaVersion: 1, Environment: environment, SourceRevision: sourceSHA,
		CatalogRevision: catalogRevision, ConfigDigest: digestI18n(configBytes),
		CACProjectionDigest: cacDigest, EntryCount: len(entries), KeySetDigest: digestI18n(keySetBytes),
		Files: []i18nSourceFile{
			{Path: "apps/frontend/src/messages/en.json", SHA256: digestI18n(messagesBytes)},
			{Path: "apps/frontend/src/messages/source-refs.json", SHA256: registryDigest},
			{Path: "apps/frontend/src/messages/zh-TW.json", SHA256: digestI18n(seedMapBytes)},
			{Path: "apps/frontend/src/messages/seed-provenance.json", SHA256: digestI18n(seedProvenanceBytes)},
			{Path: "deploy/cac/ssot.pkl#i18n-cac-projection", SHA256: cacDigest},
		},
	}
	buildConfig := i18nBuildConfig{
		SchemaVersion: 1, Environment: environment, Target: "i18n",
		SourceRevision: sourceSHA, CatalogRevision: catalogRevision, I18n: projection.I18n,
	}
	input := i18nTranslationInput{
		SchemaVersion: 1, SourceRevision: sourceSHA, CatalogRevision: catalogRevision,
		BaseLocale: projection.I18n.BaseLocale, Entries: entries,
	}
	inputBytes, err := json.Marshal(input)
	if err != nil || len(inputBytes) > maxI18nSourceBytes {
		return errors.New("assembled i18n translation input exceeds the size limit")
	}
	files := map[string]any{
		"pipeline.json":          buildConfig,
		"translation-input.json": input,
		"base.json":              base,
		"source-manifest.json":   manifest,
	}
	if err := writeI18nDirectory(output, catalogRevision, files); err != nil {
		return err
	}
	fmt.Printf("prepared i18n source entries=%d locales=%d catalog_revision=%s environment=%s\n",
		len(entries), len(projection.I18n.TargetLocales), catalogRevision, environment)
	return nil
}

func validateI18nConfig(config i18nConfig, environment string) error {
	if config.SchemaVersion != 1 || config.BaseLocale == "" || config.DefaultLocale == "" ||
		config.PromptVersion == "" || len(config.TargetLocales) == 0 || len(config.TargetLocales) > 16 {
		return errors.New("i18n config has an invalid schema or locale list")
	}
	targets := make(map[string]bool, len(config.TargetLocales))
	for _, token := range config.TargetLocales {
		if !i18nLocaleToken.MatchString(token) || targets[token] {
			return errors.New("i18n target locales contain an invalid or duplicate token")
		}
		targets[token] = true
	}
	if !targets[config.DefaultLocale] || !targets[config.BaseLocale] || len(config.LocaleMetadata) != len(targets) {
		return errors.New("i18n locale metadata does not match the target list")
	}
	seenUI := map[string]bool{}
	seenTokens := map[string]bool{}
	for _, locale := range config.LocaleMetadata {
		if !targets[locale.Token] || seenTokens[locale.Token] || locale.UILocale == "" ||
			locale.IntlLocale == "" || locale.Label == "" || seenUI[locale.UILocale] {
			return errors.New("i18n locale metadata is invalid")
		}
		seenTokens[locale.Token], seenUI[locale.UILocale] = true, true
	}
	translation := config.Translation
	u, err := url.Parse(translation.Endpoint)
	if translation.Provider != "custom" || translation.Protocol != "openai_chat_completions" ||
		translation.Model == "" || translation.RequestTimeoutSeconds <= 0 || translation.RequestTimeoutSeconds > 120 ||
		translation.MaxOutputTokens <= 0 || translation.MaxOutputTokens > 4096 || err != nil ||
		u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("i18n translation config is invalid")
	}
	secret := translation.Secret
	if secret.Target != "DEEPSEEK_API_KEY" ||
		(secret.Source != "environment" && secret.Source != "secret-manager") ||
		(secret.Source == "environment" && secret.EnvName == "") ||
		(secret.Source == "secret-manager" && !secretResourcePattern.MatchString(secret.Resource)) {
		return errors.New("i18n translation secret reference is invalid")
	}
	if environment != "local" && environment != "dev" && environment != "prod" {
		return errors.New("i18n environment is invalid")
	}
	return nil
}

func validateI18NMCPSource(source i18nMCPSource) error {
	if strings.TrimSpace(source.Instructions) == "" || strings.TrimSpace(source.ToolDescriptions.QueryProject) == "" ||
		strings.TrimSpace(source.ToolDescriptions.AddRaw) == "" || strings.TrimSpace(source.ToolDescriptions.ImportURL) == "" {
		return errors.New("i18n CaC projection is incomplete")
	}
	return nil
}

func assembleI18nEntries(flat map[string]string, registry i18nRegistry, mcp i18nMCPSource, config i18nConfig) ([]i18nEntry, map[string]string, error) {
	keys := make([]string, 0, len(flat))
	for key := range flat {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	base := make(map[string]string, len(flat))
	entries := make([]i18nEntry, 0, len(flat))
	usedRules := make(map[string]bool, len(registry.Entries))
	for _, key := range keys {
		text := flat[key]
		rule, hasRule := registry.Entries[key]
		marker := i18nCACMarker.FindStringSubmatch(text)
		origin := i18nOrigin{Kind: "literal", Path: "apps/frontend/src/messages/en.json"}
		if marker != nil {
			if !hasRule || rule.Kind != "cac" || rule.Ref != marker[1] {
				return nil, nil, fmt.Errorf("i18n entry %s has an undeclared CaC locator", key)
			}
			resolved, ok := i18nCACValue(mcp, rule.Ref)
			if !ok {
				return nil, nil, fmt.Errorf("i18n entry %s references an unavailable CaC value", key)
			}
			text = resolved
			origin = i18nOrigin{Kind: "cac", Path: "deploy/cac/ssot.pkl", Ref: rule.Ref}
		} else if hasRule && rule.Kind == "cac" {
			return nil, nil, fmt.Errorf("i18n entry %s does not match its CaC locator rule", key)
		}
		if len(text) > maxI18nEntryBytes {
			return nil, nil, fmt.Errorf("i18n entry %s exceeds the text size limit", key)
		}
		sourceLocale := config.BaseLocale
		context := "User interface copy: " + strings.Split(key, ".")[0]
		protected := make([]string, 0)
		if hasRule {
			usedRules[key] = true
			if rule.Kind != "literal" && rule.Kind != "cac" {
				return nil, nil, fmt.Errorf("i18n entry %s has an invalid source rule", key)
			}
			if rule.SourceLocale != "" {
				sourceLocale = rule.SourceLocale
			}
			if rule.Context != "" {
				context = rule.Context
			}
			protected = append(protected, rule.ProtectedLiterals...)
			for _, value := range protected {
				if value == "" || !strings.Contains(text, value) {
					return nil, nil, fmt.Errorf("i18n entry %s has an invalid protected literal", key)
				}
			}
		}
		paramNames := extractI18nParams(text, protected)
		if len(paramNames) > 0 && (!hasRule || rule.Params == nil) {
			return nil, nil, fmt.Errorf("i18n entry %s has undeclared runtime parameters", key)
		}
		if hasRule && rule.Params != nil {
			if !sameStringMultiset(rule.Params, paramNames) {
				return nil, nil, fmt.Errorf("i18n entry %s has undeclared runtime parameters", key)
			}
			paramNames = append([]string(nil), rule.Params...)
		}
		if !hasI18nLocale(config, sourceLocale) || context == "" || strings.TrimSpace(context) != context {
			return nil, nil, fmt.Errorf("i18n entry %s has invalid source metadata", key)
		}
		digestInput, err := json.Marshal(i18nDigestEntry{
			Key: key, Text: text, SourceLocale: sourceLocale, Params: paramNames,
			ProtectedLiterals: protected,
		})
		if err != nil {
			return nil, nil, errors.New("i18n entry identity cannot be encoded")
		}
		entry := i18nEntry{
			Key: key, Text: text, SourceLocale: sourceLocale, Context: context, Params: paramNames,
			ProtectedLiterals: protected, SourceDigest: digestI18n(digestInput), Origin: origin,
		}
		entries = append(entries, entry)
		base[key] = text
	}
	for key := range registry.Entries {
		if !usedRules[key] {
			return nil, nil, fmt.Errorf("i18n source rule %s has no matching base entry", key)
		}
	}
	return entries, base, nil
}

func hasI18nLocale(config i18nConfig, token string) bool {
	for _, locale := range config.LocaleMetadata {
		if locale.Token == token {
			return true
		}
	}
	return false
}

func extractI18nParams(text string, protected []string) []string {
	for _, literal := range protected {
		text = strings.ReplaceAll(text, literal, "")
	}
	matches := i18nParam.FindAllStringSubmatch(text, -1)
	params := make([]string, len(matches))
	for i, match := range matches {
		params[i] = match[1]
	}
	return params
}

func i18nCACValue(source i18nMCPSource, ref string) (string, bool) {
	switch ref {
	case "mcp.instructions":
		return source.Instructions, true
	case "mcp.tool_descriptions.query_project":
		return source.ToolDescriptions.QueryProject, true
	case "mcp.tool_descriptions.add_raw":
		return source.ToolDescriptions.AddRaw, true
	case "mcp.tool_descriptions.import_url":
		return source.ToolDescriptions.ImportURL, true
	default:
		return "", false
	}
}

func sameStringMultiset(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	counts := make(map[string]int, len(left))
	for _, value := range left {
		counts[value]++
	}
	for _, value := range right {
		counts[value]--
		if counts[value] < 0 {
			return false
		}
	}
	return true
}

func flattenI18nSource(node map[string]any, prefix string, output map[string]string) error {
	for key, value := range node {
		if strings.TrimSpace(key) == "" || strings.Contains(key, ".") {
			return errors.New("i18n base source contains an ambiguous path")
		}
		fullKey := key
		if prefix != "" {
			fullKey = prefix + "." + key
		}
		if len(fullKey) > maxI18nKeyBytes {
			return errors.New("i18n base source key exceeds the size limit")
		}
		switch child := value.(type) {
		case map[string]any:
			if err := flattenI18nSource(child, fullKey, output); err != nil {
				return err
			}
		case string:
			if _, exists := output[fullKey]; exists {
				return errors.New("i18n base source contains a path collision")
			}
			output[fullKey] = child
		default:
			return fmt.Errorf("i18n base entry %s must contain text", fullKey)
		}
	}
	return nil
}

func readI18nSource(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxI18nSourceBytes {
		return nil, errors.New("source file is unavailable or exceeds the size limit")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("source file is unavailable")
	}
	return data, nil
}

func decodeI18nUniqueJSON(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := readI18nJSONValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("trailing JSON data")
	}
	return value, nil
}

func readI18nJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delim {
	case '{':
		object := make(map[string]any)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, errors.New("invalid JSON object key")
			}
			if _, exists := object[key]; exists {
				return nil, fmt.Errorf("duplicate JSON key %s", key)
			}
			value, err := readI18nJSONValue(decoder)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return nil, errors.New("invalid JSON object terminator")
		}
		return object, nil
	case '[':
		var array []any
		for decoder.More() {
			value, err := readI18nJSONValue(decoder)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return nil, errors.New("invalid JSON array terminator")
		}
		return array, nil
	default:
		return nil, errors.New("unexpected JSON delimiter")
	}
}

func writeI18nDirectory(output, revision string, files map[string]any) error {
	if strings.TrimSpace(output) == "" {
		return errors.New("i18n output directory is required")
	}
	if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(revision) {
		return errors.New("i18n catalog revision is invalid")
	}
	output = filepath.Clean(output)
	if err := os.MkdirAll(output, 0o755); err != nil {
		return fmt.Errorf("create i18n output directory: %w", err)
	}
	info, err := os.Lstat(output)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("i18n output must be a real directory")
	}
	i18nRoot := filepath.Join(output, "i18n")
	revisionRoot := filepath.Join(i18nRoot, "revisions")
	if err := os.MkdirAll(revisionRoot, 0o755); err != nil {
		return fmt.Errorf("create i18n revision directory: %w", err)
	}
	for _, path := range []string{i18nRoot, revisionRoot} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("i18n revision root must be a real directory")
		}
	}
	stage, err := os.MkdirTemp(revisionRoot, ".staging-")
	if err != nil {
		return fmt.Errorf("create i18n staging directory: %w", err)
	}
	defer os.RemoveAll(stage)
	for name, value := range files {
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return errors.New("encode i18n artifact")
		}
		if err := os.WriteFile(filepath.Join(stage, name), append(data, '\n'), 0o644); err != nil {
			return fmt.Errorf("write i18n artifact %s: %w", name, err)
		}
	}
	final := filepath.Join(i18nRoot, "revisions", revision)
	if _, err := os.Lstat(final); err == nil {
		if err := verifyI18nRevisionDirectory(final, files); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("i18n revision path cannot be inspected")
	} else if err := os.Rename(stage, final); err != nil {
		if _, statErr := os.Lstat(final); statErr != nil {
			return fmt.Errorf("publish i18n revision: %w", err)
		}
		if verifyErr := verifyI18nRevisionDirectory(final, files); verifyErr != nil {
			return verifyErr
		}
	}
	return writeI18nCurrent(i18nRoot, revision)
}

func verifyI18nRevisionDirectory(path string, files map[string]any) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("i18n revision path is not a real directory")
	}
	for name := range files {
		artifactPath := filepath.Join(path, name)
		artifact, err := os.Lstat(artifactPath)
		if err != nil || !artifact.Mode().IsRegular() || artifact.Size() <= 0 || artifact.Size() > maxI18nSourceBytes {
			return fmt.Errorf("i18n revision is missing a valid %s artifact", name)
		}
		expected, err := json.MarshalIndent(files[name], "", "  ")
		if err != nil {
			return errors.New("encode expected i18n revision artifact")
		}
		actual, err := os.ReadFile(artifactPath)
		if err != nil || !bytes.Equal(actual, append(expected, '\n')) {
			return fmt.Errorf("i18n revision artifact %s does not match its identity", name)
		}
	}
	return nil
}

func writeI18nCurrent(root, revision string) error {
	temp, err := os.CreateTemp(root, ".current-")
	if err != nil {
		return fmt.Errorf("create i18n current pointer: %w", err)
	}
	name := temp.Name()
	defer os.Remove(name)
	if _, err := temp.WriteString(revision + "\n"); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write i18n current pointer: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close i18n current pointer: %w", err)
	}
	if err := os.Rename(name, filepath.Join(root, "CURRENT")); err != nil {
		return fmt.Errorf("publish i18n current pointer: %w", err)
	}
	return nil
}

func currentI18nDirectory(output string) (string, error) {
	root := filepath.Join(filepath.Clean(output), "i18n")
	pointer := filepath.Join(root, "CURRENT")
	info, err := os.Lstat(pointer)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("i18n current revision is unavailable")
	}
	data, err := os.ReadFile(pointer)
	if err != nil {
		return "", errors.New("i18n current revision is unavailable")
	}
	revision := strings.TrimSpace(string(data))
	if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(revision) {
		return "", errors.New("i18n current revision is invalid")
	}
	path := filepath.Join(root, "revisions", revision)
	if info, err := os.Lstat(path); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("i18n current revision directory is unavailable")
	}
	return path, nil
}

func digestI18n(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
