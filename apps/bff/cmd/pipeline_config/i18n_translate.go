package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type i18nTranslationOptions struct {
	ConfigPath     string
	InputPath      string
	SourceManifest string
	BasePath       string
	Output         string
	FrontendRoot   string
	RepositoryRoot string
	SeedMap        string
	SeedProvenance string
	Cache          string
	Fixture        string
	Offline        bool
}

type i18nTranslationReceipt struct {
	SchemaVersion   int    `json:"schema_version"`
	SourceRevision  string `json:"source_revision"`
	CatalogRevision string `json:"catalog_revision"`
	ProviderStatus  string `json:"provider_status"`
	Provider        string `json:"configured_provider"`
	Protocol        string `json:"protocol"`
	EndpointHost    string `json:"endpoint_host"`
	RequestedModel  string `json:"requested_model"`
	SecretSource    string `json:"secret_source"`
	SecretTarget    string `json:"secret_target"`
	SecretReference string `json:"secret_reference,omitempty"`
	SecretVersion   string `json:"resolved_secret_version,omitempty"`
}

func runTranslateI18n(ctx context.Context, options i18nTranslationOptions, newReader readerFactory) error {
	if options.Offline && options.Fixture != "" {
		return errors.New("--offline and --fixture cannot be combined")
	}
	for _, value := range []string{options.ConfigPath, options.InputPath, options.SourceManifest,
		options.BasePath, options.Output, options.FrontendRoot} {
		if strings.TrimSpace(value) == "" {
			return errors.New("i18n translation paths are required")
		}
	}
	root := strings.TrimSpace(options.RepositoryRoot)
	if root == "" {
		var err error
		root, err = repositoryRoot()
		if err != nil {
			return err
		}
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return errors.New("i18n repository root is invalid")
	}
	seedMap := options.SeedMap
	if seedMap == "" {
		seedMap = filepath.Join(root, "apps/frontend/src/messages/zh-TW.json")
	}
	seedProvenance := options.SeedProvenance
	if seedProvenance == "" {
		seedProvenance = filepath.Join(root, "apps/frontend/src/messages/seed-provenance.json")
	}
	configPath := filepath.Clean(options.ConfigPath)
	inputPath := filepath.Clean(options.InputPath)
	manifestPath := filepath.Clean(options.SourceManifest)
	basePath := filepath.Clean(options.BasePath)
	artifactDir := filepath.Dir(configPath)
	if filepath.Base(artifactDir) == "." || filepath.Dir(inputPath) != artifactDir ||
		filepath.Dir(manifestPath) != artifactDir || filepath.Dir(basePath) != artifactDir {
		return errors.New("i18n source artifacts must share one immutable revision directory")
	}
	for _, path := range []string{configPath, inputPath, manifestPath, basePath} {
		if _, err := readI18nSource(path); err != nil {
			return errors.New("i18n source artifact is unavailable")
		}
	}
	configBytes, err := readI18nSource(configPath)
	if err != nil {
		return errors.New("i18n pipeline config is unavailable")
	}
	var config i18nBuildConfig
	decoder := json.NewDecoder(bytes.NewReader(configBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil || config.SchemaVersion != 1 || config.Target != "i18n" ||
		!i18nSourceSHA.MatchString(config.SourceRevision) || !regexpI18nDigest.MatchString(config.CatalogRevision) ||
		filepath.Base(artifactDir) != config.CatalogRevision {
		return errors.New("i18n pipeline config identity is invalid")
	}
	if err := validateI18nConfig(config.I18n, config.Environment); err != nil {
		return err
	}
	translation := config.I18n.Translation
	providerStatus := "ready"
	secretVersion := ""
	var secretValue []byte
	if !options.Offline && options.Fixture == "" {
		secretValue, secretVersion, err = resolveBinding(ctx, translation.Secret, newReader)
		if err != nil {
			clear(secretValue)
			secretValue = nil
			providerStatus = "credential_unavailable"
		}
	} else {
		providerStatus = "not_configured"
	}
	defer clear(secretValue)
	pythonArgs := []string{
		filepath.Join(root, "apps/frontend/scripts/prepare-i18n.py"),
		"--config", configPath, "--input", inputPath,
		"--source-manifest", manifestPath, "--base", basePath,
		"--seed-map", seedMap,
		"--seed-provenance", seedProvenance,
		"--output", options.Output, "--frontend-root", options.FrontendRoot,
		"--repository-root", root, "--provider-status", providerStatus,
	}
	if options.Cache != "" {
		pythonArgs = append(pythonArgs, "--cache", options.Cache)
	}
	if options.Fixture != "" {
		pythonArgs = append(pythonArgs, "--fixture", options.Fixture)
	}
	if len(secretValue) > 0 {
		pythonArgs = append(pythonArgs, "--api-key-stdin")
	}
	stageCtx, cancel := context.WithTimeout(ctx, 16*time.Minute)
	defer cancel()
	command := exec.CommandContext(stageCtx, "python3", pythonArgs...)
	command.Dir = root
	command.Stdin = bytes.NewReader(secretValue)
	var stageStdout bytes.Buffer
	var stageStderr bytes.Buffer
	command.Stdout, command.Stderr = &stageStdout, &stageStderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("run i18n translation stage: %w: %s", err, strings.TrimSpace(stageStderr.String()))
	}
	var summary struct {
		CatalogRevision string `json:"catalog_revision"`
		BundleRevision  string `json:"bundle_revision"`
	}
	if err := json.Unmarshal(stageStdout.Bytes(), &summary); err != nil ||
		summary.CatalogRevision != config.CatalogRevision || !regexpI18nDigest.MatchString(summary.BundleRevision) {
		return errors.New("i18n translation stage returned an invalid catalog identity")
	}
	endpoint, _ := url.Parse(translation.Endpoint)
	receipt := i18nTranslationReceipt{
		SchemaVersion: 1, SourceRevision: config.SourceRevision, CatalogRevision: config.CatalogRevision,
		ProviderStatus: providerStatus, Provider: translation.Provider, Protocol: translation.Protocol,
		EndpointHost: endpoint.Hostname(), RequestedModel: translation.Model,
		SecretSource: translation.Secret.Source, SecretTarget: translation.Secret.Target,
		SecretReference: translation.Secret.Resource, SecretVersion: secretVersion,
	}
	if err := writeI18nTranslationReceipt(options.Output, receipt); err != nil {
		return err
	}
	if _, err := io.Copy(os.Stdout, bytes.NewReader(stageStdout.Bytes())); err != nil {
		return errors.New("write i18n stage summary")
	}
	return nil
}

func runTranslateI18nCLI(ctx context.Context, args []string, newReader readerFactory) error {
	root, err := repositoryRoot()
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("translate-i18n", flag.ExitOnError)
	options := i18nTranslationOptions{}
	flags.StringVar(&options.ConfigPath, "config", "", "immutable i18n pipeline config")
	flags.StringVar(&options.InputPath, "input", "", "immutable i18n translation input")
	flags.StringVar(&options.SourceManifest, "source-manifest", "", "i18n source manifest")
	flags.StringVar(&options.BasePath, "base", "", "assembled fallback base map")
	flags.StringVar(&options.Output, "output", "", "catalog and private receipt directory")
	flags.StringVar(&options.FrontendRoot, "frontend-root", "", "frontend project root for build inputs")
	flags.StringVar(&options.RepositoryRoot, "repository-root", root, "repository root")
	flags.StringVar(&options.SeedMap, "seed-map", filepath.Join(root, "apps/frontend/src/messages/zh-TW.json"), "Traditional Chinese seed map")
	flags.StringVar(&options.SeedProvenance, "seed-provenance", filepath.Join(root, "apps/frontend/src/messages/seed-provenance.json"), "Traditional Chinese seed provenance")
	flags.StringVar(&options.Cache, "cache", "", "private validated translation cache directory")
	flags.StringVar(&options.Fixture, "fixture", "", "fixture translations for local QA")
	flags.BoolVar(&options.Offline, "offline", false, "skip provider credentials and use verified seeds plus base fallback")
	if err := flags.Parse(args); err != nil {
		return err
	}
	return runTranslateI18n(ctx, options, newReader)
}

func writeI18nTranslationReceipt(output string, receipt i18nTranslationReceipt) error {
	if err := os.MkdirAll(output, 0o755); err != nil {
		return errors.New("create i18n translation output directory")
	}
	info, err := os.Lstat(output)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("i18n translation output directory is invalid")
	}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return errors.New("encode private i18n translation receipt")
	}
	temp, err := os.CreateTemp(output, ".translation-receipt-")
	if err != nil {
		return errors.New("create private i18n translation receipt")
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return errors.New("secure private i18n translation receipt")
	}
	if _, err := temp.Write(append(data, '\n')); err != nil {
		_ = temp.Close()
		return errors.New("write private i18n translation receipt")
	}
	if err := temp.Close(); err != nil {
		return errors.New("close private i18n translation receipt")
	}
	if err := os.Rename(name, filepath.Join(output, "translation-receipt.json")); err != nil {
		return errors.New("publish private i18n translation receipt")
	}
	return nil
}
