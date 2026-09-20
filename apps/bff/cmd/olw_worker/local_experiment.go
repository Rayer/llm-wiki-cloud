package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/rayer/llm-wiki-bff/internal/llm"
	"github.com/rayer/llm-wiki-bff/internal/suggestedqueries"
	"github.com/spf13/cobra"
)

// This narrow local-only adapter deliberately bypasses cloud routing and publish
// machinery. Its caller owns an exclusive copied experiment workspace.
func newLocalExperimentCommand() *cobra.Command {
	var vault, syntoBinary string
	cmd := &cobra.Command{Use: "local-experiment <index|suggested>", Hidden: true, SilenceUsage: true, SilenceErrors: true, Args: fixedArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if args[0] != "index" && args[0] != "suggested" {
			return errors.New("unknown experiment stage")
		}
		if vault == "" {
			return errors.New("local experiment vault required")
		}
		if _, err := readBoundedRegularFileWithin(vault, ".lwc-experiment-workspace"); err != nil {
			return errors.New("exclusive local experiment workspace required")
		}
		if err := validateSyntoVaultLayout(vault); err != nil {
			return err
		}
		log.SetOutput(io.Discard)
		if args[0] == "index" {
			if err := validateSyntoPipelineSafety(filepath.Join(vault, "synto.toml")); err != nil {
				return err
			}
			env := []string{"XDG_CONFIG_HOME=" + os.Getenv("XDG_CONFIG_HOME")}
			if os.Getenv("XDG_CONFIG_HOME") == "" {
				return errors.New("private XDG_CONFIG_HOME required")
			}
			if syntoBinary == "" {
				syntoBinary = "synto"
			}
			privateHome := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "home")
			privateTemp := filepath.Join(privateHome, "tmp")
			if err := os.MkdirAll(privateTemp, 0700); err != nil {
				return err
			}
			// Same public executable boundary as production execOLWCommand, with
			// local-only settings preventing writes into the installed Python tool.
			original := execOLW
			defer func() { execOLW = original }()
			execOLW = func(ctx context.Context, vault string, command, env []string, stdout, stderr io.Writer) error {
				child := exec.CommandContext(ctx, syntoBinary, command...)
				child.Dir, child.Env, child.Stdout, child.Stderr = vault, allowlistedSyntoEnvironment(env), stdout, stderr
				child.Env = append(child.Env, "PYTHONDONTWRITEBYTECODE=1", "HOME="+privateHome, "TMPDIR="+privateTemp, "XDG_CACHE_HOME="+filepath.Join(privateHome, ".cache"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
				return child.Run()
			}
			// Export unconditionally: an existing INDEX must not mask a fresh stage.
			data, err := exportSyntoIndex(cmd.Context(), vault, env, io.Discard, io.Discard)
			if err != nil {
				return errors.New("Synto identity export failed")
			}
			if err := writeFileAtomicWithin(vault, ".synto/INDEX.json", data); err != nil {
				return err
			}
			if _, err := readSyntoIndexTruth(vault); err != nil {
				return err
			}
			return runCacheIndexStage(cmd.Context(), vault, io.Discard)
		}
		key := os.Getenv("DEEPSEEK_API_KEY")
		if key == "" {
			return errors.New("DEEPSEEK_API_KEY required for suggested queries")
		}
		// A selected fresh stage cannot pass by retaining a previous generation.
		if err := os.Remove(filepath.Join(vault, suggestedqueries.Path)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		category := ""
		err := writeSuggestedQueriesObserved(cmd.Context(), vault, llm.NewClient(key), func(value string) { category = value })
		if category != "" {
			return localSuggestedFailure(cmd.OutOrStdout(), category)
		}
		if err != nil {
			return localSuggestedFailure(cmd.OutOrStdout(), "artifact_io_failed")
		}
		data, err := readBoundedRegularFileWithin(vault, suggestedqueries.Path)
		if err != nil {
			return err
		}
		artifact, err := suggestedqueries.Decode(data)
		if err != nil {
			return localSuggestedFailure(cmd.OutOrStdout(), "artifact_schema_invalid")
		}
		if err = suggestedqueries.ValidatePublishedArtifact(artifact); err != nil || len(artifact.Queries) != suggestedqueries.RequiredQueries {
			return localSuggestedFailure(cmd.OutOrStdout(), "artifact_cardinality_or_validation_failed")
		}
		return nil
	}}
	cmd.Flags().StringVar(&syntoBinary, "synto-bin", "", "public Synto executable for offline export")
	cmd.Flags().StringVar(&vault, "local-vault", "", "exclusive copied local experiment vault")
	return cmd
}

func localSuggestedFailure(out io.Writer, category string) error {
	// Categories originate only from bounded classifiers, never arbitrary errors.
	_ = json.NewEncoder(out).Encode(map[string]string{"error_type": "suggested_" + category})
	return errors.New("fresh suggested-query generation failed")
}
