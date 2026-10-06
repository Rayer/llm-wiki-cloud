package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

func readLocalPipelineAPIKey(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > maxDeployedPipelineConfigBytes {
		return nil, errors.New("local Pipeline private bindings file is unavailable or not private")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("local Pipeline private bindings file is unavailable or not private")
	}
	var bindings struct {
		Environment string `json:"environment"`
		LocalAPIKey []byte `json:"localApiKey"`
	}
	if json.Unmarshal(data, &bindings) != nil || bindings.Environment != "local" || len(bindings.LocalAPIKey) > maxWorkerKeyBytes {
		clear(bindings.LocalAPIKey)
		return nil, errors.New("local Pipeline private bindings file is invalid")
	}
	return bindings.LocalAPIKey, nil
}

func readLocalPipelineInputs(configPath, bindingsPath string) ([]byte, []byte, int, error) {
	if strings.TrimSpace(configPath) == "" || strings.TrimSpace(bindingsPath) == "" {
		return nil, nil, 0, errors.New("local Pipeline config and private bindings must be configured together")
	}
	info, err := os.Lstat(configPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxDeployedPipelineConfigBytes {
		return nil, nil, 0, errors.New("local Pipeline config file is unavailable or invalid")
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, nil, 0, errors.New("local Pipeline config file is unavailable or invalid")
	}
	apiKey, err := readLocalPipelineAPIKey(bindingsPath)
	if err != nil {
		return nil, nil, 0, err
	}
	seconds, err := pipelineRunTimeout(data)
	if err != nil {
		clear(apiKey)
		return nil, nil, 0, err
	}
	return data, apiKey, seconds, nil
}

func readCloudPipelineInputs(ctx context.Context, cfg workerConfig, objects objectStore) ([]byte, []byte, int, error) {
	if cfg.LocalCloudScope == "" {
		if cfg.PipelineConfigPath != "" || cfg.PipelineBindingsPath != "" {
			return nil, nil, 0, errCloudWorkerConfigInvalid
		}
		data, seconds, err := readDeployedPipelineConfig(ctx, objects)
		return data, nil, seconds, err
	}
	data, apiKey, seconds, err := readLocalPipelineInputs(cfg.PipelineConfigPath, cfg.PipelineBindingsPath)
	if err != nil {
		return nil, nil, 0, err
	}
	return data, apiKey, seconds, nil
}

func readDeployedPipelineConfig(ctx context.Context, objects objectStore) ([]byte, int, error) {
	data, _, err := objects.Read(ctx, deployedPipelineConfigObjectPath, 0, maxDeployedPipelineConfigBytes)
	if err != nil {
		return nil, 0, fmt.Errorf("read deployed Pipeline config: %w", err)
	}
	seconds, err := pipelineRunTimeout(data)
	if err != nil {
		return nil, 0, err
	}
	return data, seconds, nil
}

const (
	deployedPipelineConfigObjectPath = "pipeline-config/synto.toml"
	maxDeployedPipelineConfigBytes   = 1 << 20
)

func pipelineRunTimeout(data []byte) (int, error) {
	if len(data) == 0 || len(data) > maxDeployedPipelineConfigBytes {
		return 0, errors.New("deployed synto.toml is empty or exceeds the configuration limit")
	}
	var config struct {
		Pipeline struct {
			RunTimeoutSeconds int `toml:"run_timeout_seconds"`
		} `toml:"pipeline"`
	}
	if _, err := toml.Decode(string(data), &config); err != nil {
		return 0, fmt.Errorf("parse deployed synto.toml: %w", err)
	}
	if config.Pipeline.RunTimeoutSeconds <= 0 ||
		int64(config.Pipeline.RunTimeoutSeconds) > (int64(1<<63-1)/int64(time.Second)) {
		return 0, errors.New("deployed synto.toml must set pipeline.run_timeout_seconds to a positive value")
	}
	return config.Pipeline.RunTimeoutSeconds, nil
}
