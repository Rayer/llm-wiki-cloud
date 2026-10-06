package localpipeline

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	cloudfirestore "cloud.google.com/go/firestore"
	cloudstorage "cloud.google.com/go/storage"
	"github.com/rayer/llm-wiki-bff/internal/auth"
	scopedfirestore "github.com/rayer/llm-wiki-bff/internal/firestore"
	"github.com/rayer/llm-wiki-bff/internal/gcs"
	"github.com/rayer/llm-wiki-bff/internal/generation"
	handlerapi "github.com/rayer/llm-wiki-bff/internal/handler"
	"github.com/rayer/llm-wiki-bff/internal/localcloud"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var ErrAlreadyRunning = errors.New("pipeline is already running")
var ErrManagerClosed = errors.New("pipeline manager is closing")
var ErrExecutionNotFound = handlerapi.ErrPipelineExecutionNotFound

type Config struct {
	Firestore            *cloudfirestore.Client
	Storage              *gcs.Client
	Worker               string
	Stderr               io.Writer
	Project              string
	Bucket               string
	Database             string
	Scope                string
	WorkDir              string
	PipelineConfigPath   string
	PipelineBindingsPath string
}

type Manager struct {
	cfg       Config
	mu        sync.Mutex
	processes map[string]*process
	closing   bool
}

type process struct {
	cmd  *exec.Cmd
	done chan struct{}
}

func New(cfg Config) (*Manager, error) {
	if cfg.Firestore == nil || cfg.Storage == nil || strings.TrimSpace(cfg.Worker) == "" || strings.TrimSpace(cfg.Scope) == "" {
		return nil, errors.New("local pipeline configuration is incomplete")
	}
	if (strings.TrimSpace(cfg.PipelineConfigPath) == "") != (strings.TrimSpace(cfg.PipelineBindingsPath) == "") {
		return nil, errors.New("local Pipeline config and private bindings must be configured together")
	}
	if err := scopedfirestore.RegisterLocalScope(cfg.Firestore, cfg.Scope); err != nil {
		return nil, err
	}
	return &Manager{cfg: cfg, processes: make(map[string]*process)}, nil
}

// ReconcileInterrupted marks prior RUNNING records unknown. A restarted BFF
// cannot infer publication from a child exit that it did not observe.
func (m *Manager) ReconcileInterrupted(ctx context.Context) error {
	locks, err := scopedfirestore.Collection(m.cfg.Firestore, "local_pipeline_locks").Where("status", "==", "RUNNING").Documents(ctx).GetAll()
	if err != nil {
		return err
	}
	for _, lock := range locks {
		data := lock.Data()
		executionID, _ := data["execution_id"].(string)
		if executionID == "" {
			continue
		}
		ref := scopedfirestore.Collection(m.cfg.Firestore, "executions").Doc(executionID)
		if _, err := ref.Update(ctx, []cloudfirestore.Update{{Path: "status", Value: "UNKNOWN"}, {Path: "failure_reason", Value: "BFF restarted before the worker result could be confirmed"}}); err != nil && status.Code(err) != codes.NotFound {
			return err
		}
		if _, err := lock.Ref.Update(ctx, []cloudfirestore.Update{{Path: "status", Value: "UNKNOWN"}, {Path: "updated_at", Value: time.Now().UTC()}}); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) Start(ctx context.Context, userID, projectID, stage string, cleanRebuild bool) (string, error) {
	if !auth.ValidPathSegment(userID) || !auth.ValidPathSegment(projectID) {
		return "", errors.New("invalid Project identity")
	}
	if stage != "full" && stage != "suggested-queries" || cleanRebuild && stage != "full" {
		return "", errors.New("unsupported local pipeline stage")
	}
	manifest, _, exists, err := m.cfg.Storage.WithScope(userID, projectID).CurrentManifest(ctx)
	if err != nil {
		return "", fmt.Errorf("read current publication before worker start: %w", err)
	}
	executionID, err := newExecutionID()
	if err != nil {
		return "", err
	}
	started := time.Now().UTC()
	lockID := userID + "__" + projectID
	lockRef := scopedfirestore.Collection(m.cfg.Firestore, "local_pipeline_locks").Doc(lockID)
	executionRef := scopedfirestore.Collection(m.cfg.Firestore, "executions").Doc(executionID)
	beforeID := ""
	if exists {
		beforeID = manifest.GenerationID
	}
	err = m.cfg.Firestore.RunTransaction(ctx, func(ctx context.Context, tx *cloudfirestore.Transaction) error {
		lock, getErr := tx.Get(lockRef)
		if getErr == nil {
			lockData := lock.Data()
			lockStatus, _ := lockData["status"].(string)
			if lockStatus == "RUNNING" || lockStatus == "UNKNOWN" {
				return ErrAlreadyRunning
			}
		} else if status.Code(getErr) != codes.NotFound {
			return getErr
		}
		if err := tx.Set(lockRef, map[string]any{"user_id": userID, "project_id": projectID, "execution_id": executionID, "status": "RUNNING", "updated_at": started}); err != nil {
			return err
		}
		return tx.Create(executionRef, map[string]any{
			"user_id": userID, "project_id": projectID, "execution_id": executionID,
			"task_type": "pipeline", "native": true, "stage": stage,
			"status": "RUNNING", "started_at": started,
			"manifest_generation_before": beforeID,
		})
	})
	if err != nil {
		return "", err
	}

	cmd := newWorkerCommand(m.cfg, executionID, userID, projectID, stage, cleanRebuild, os.Environ())
	p := &process{cmd: cmd, done: make(chan struct{})}
	if err := m.startProcess(executionID, p, cmd.Start); err != nil {
		finishCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		m.finish(finishCtx, executionID, lockRef, userID, projectID, beforeID, false, err)
		cancel()
		return "", fmt.Errorf("start local worker: %w", err)
	}
	go m.wait(p, executionID, lockRef, userID, projectID, beforeID)
	return executionID, nil
}

func (m *Manager) wait(p *process, executionID string, lockRef *cloudfirestore.DocumentRef, userID, projectID, beforeID string) {
	err := p.cmd.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	m.finish(ctx, executionID, lockRef, userID, projectID, beforeID, err == nil, err)
	m.mu.Lock()
	delete(m.processes, executionID)
	m.mu.Unlock()
	close(p.done)
}

func (m *Manager) finish(ctx context.Context, executionID string, lockRef *cloudfirestore.DocumentRef, userID, projectID, beforeID string, childSucceeded bool, childErr error) {
	storage := m.cfg.Storage.WithScope(userID, projectID)
	manifest, _, exists, manifestErr := storage.CurrentManifest(ctx)
	receiptData, receiptErr := storage.ReadLocalPublicationReceipt(ctx, executionID)
	statusValue, reason := ResolvePublicationOutcome(executionID, beforeID, manifest, exists, childSucceeded, childErr, manifestErr, receiptData, receiptErr)
	finished := time.Now().UTC()
	executionRef := scopedfirestore.Collection(m.cfg.Firestore, "executions").Doc(executionID)
	updates := []cloudfirestore.Update{{Path: "status", Value: statusValue}, {Path: "finished_at", Value: finished}, {Path: "failure_reason", Value: reason}, {Path: "exit_code", Value: processExitCode(childErr)}}
	if err := m.cfg.Firestore.RunTransaction(ctx, func(ctx context.Context, tx *cloudfirestore.Transaction) error {
		if _, err := tx.Get(executionRef); err != nil {
			return err
		}
		lock, err := tx.Get(lockRef)
		if err != nil {
			return err
		}
		if err := tx.Update(executionRef, updates); err != nil {
			return err
		}
		if lock.Data()["execution_id"] != executionID {
			return nil
		}
		return tx.Update(lockRef, []cloudfirestore.Update{{Path: "status", Value: statusValue}, {Path: "updated_at", Value: finished}})
	}); err != nil {
		// Keep this diagnostic free of subprocess output, which can contain user
		// content. A later BFF startup will reconcile the still-running lock.
		fmt.Fprintf(os.Stderr, "local pipeline result write failed for %s\n", executionID)
	}
}

// ResolvePublicationOutcome uses the execution receipt when available and
// falls back to the execution identity embedded in the committed current
// manifest. The manifest identity is part of the CAS write, so a later
// post-commit receipt error cannot erase evidence that this execution published.
func ResolvePublicationOutcome(executionID, beforeID string, current generation.Manifest, currentExists bool, childSucceeded bool, childErr error, manifestErr error, receiptData []byte, receiptErr error) (string, string) {
	if receiptErr == nil {
		receipt, err := localcloud.DecodePublicationReceipt(receiptData)
		if err != nil || receipt.ExecutionID != executionID {
			return "UNKNOWN", "execution publication receipt could not be verified"
		}
		reason := ""
		if childErr != nil || !childSucceeded {
			reason = fmt.Sprintf("execution publication was committed; worker exited with code %d afterward", processExitCode(childErr))
		}
		return "SUCCEEDED", reason
	}
	if manifestErr == nil && currentExists && current.GenerationID != beforeID && current.LocalExecutionID == executionID {
		reason := ""
		if childErr != nil || !childSucceeded {
			reason = fmt.Sprintf("this execution committed its manifest; worker exited with code %d afterward", processExitCode(childErr))
		}
		return "SUCCEEDED", reason
	}
	if !errors.Is(receiptErr, cloudstorage.ErrObjectNotExist) {
		return "UNKNOWN", "execution publication receipt could not be read"
	}
	if manifestErr != nil {
		return "UNKNOWN", "publication result could not be read back from GCS"
	}
	if currentExists && current.GenerationID != beforeID {
		return "UNKNOWN", "current generation changed without this execution's publication receipt"
	}
	if childSucceeded {
		return "FAILED", "worker exited without an execution-specific committed publication receipt"
	}
	return "FAILED", fmt.Sprintf("native worker exited with code %d before confirmed publication", processExitCode(childErr))
}

func (m *Manager) startProcess(executionID string, p *process, start func() error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return ErrManagerClosed
	}
	if err := start(); err != nil {
		return err
	}
	m.processes[executionID] = p
	return nil
}

func (m *Manager) Status(ctx context.Context, userID, projectID, executionID string) (*handlerapi.PipelineExecutionResponse, error) {
	collection := scopedfirestore.Collection(m.cfg.Firestore, "executions")
	var snapshots []*cloudfirestore.DocumentSnapshot
	var err error
	if executionID != "" {
		var snapshot *cloudfirestore.DocumentSnapshot
		snapshot, err = collection.Doc(executionID).Get(ctx)
		if status.Code(err) == codes.NotFound {
			return nil, ErrExecutionNotFound
		}
		if err == nil {
			snapshots = []*cloudfirestore.DocumentSnapshot{snapshot}
		}
	} else {
		snapshots, err = collection.Where("user_id", "==", userID).Where("project_id", "==", projectID).Documents(ctx).GetAll()
	}
	if err != nil {
		return nil, err
	}
	var selected *cloudfirestore.DocumentSnapshot
	var selectedStart time.Time
	for _, snapshot := range snapshots {
		data := snapshot.Data()
		if data["user_id"] != userID || data["project_id"] != projectID || data["native"] != true {
			continue
		}
		started, _ := data["started_at"].(time.Time)
		if selected == nil || started.After(selectedStart) {
			selected, selectedStart = snapshot, started
		}
	}
	if selected == nil {
		if executionID == "" {
			return nil, nil
		}
		return nil, ErrExecutionNotFound
	}
	data := selected.Data()
	state, _ := data["status"].(string)
	response := &handlerapi.PipelineExecutionResponse{
		Name: selected.Ref.ID, Status: state,
		StartTime: formatTime(data["started_at"]), EndTime: formatTime(data["finished_at"]),
		Duration: formatDuration(data["started_at"], data["finished_at"]),
		LogURL:   "/api/v1/pipeline/log?execution_id=" + selected.Ref.ID,
	}
	if reason, _ := data["failure_reason"].(string); reason != "" {
		response.LogStateReason = reason
		code, _ := data["exit_code"].(int64)
		exitCode := int(code)
		response.Diagnostic = &handlerapi.PipelineFailureDiagnostic{Version: 1, Status: strings.ToLower(state), Stage: "native-worker", ErrorClass: "execution", DetailCode: reason, ExitCode: &exitCode}
	}
	return response, nil
}

func (m *Manager) Running(ctx context.Context, userID, projectID string) (bool, error) {
	ref := scopedfirestore.Collection(m.cfg.Firestore, "local_pipeline_locks").Doc(userID + "__" + projectID)
	snapshot, err := ref.Get(ctx)
	if status.Code(err) == codes.NotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	state, _ := snapshot.Data()["status"].(string)
	return state == "RUNNING" || state == "UNKNOWN", nil
}

func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	m.closing = true
	processes := make([]*process, 0, len(m.processes))
	for _, p := range m.processes {
		processes = append(processes, p)
	}
	m.mu.Unlock()
	for _, p := range processes {
		if p.cmd.Process != nil {
			_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)
		}
	}
	for _, p := range processes {
		select {
		case <-p.done:
		case <-ctx.Done():
			var killErrors []error
			for _, managed := range processes {
				if managed.cmd.Process == nil {
					continue
				}
				if err := syscall.Kill(-managed.cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
					killErrors = append(killErrors, err)
				}
			}
			return errors.Join(append([]error{ctx.Err()}, killErrors...)...)
		}
	}
	return nil
}

func processExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func workerArgs(stage string) []string {
	if stage == "suggested-queries" {
		return []string{"suggested-queries"}
	}
	return []string{"run", `[["run","--auto-approve"]]`}
}

func newWorkerCommand(cfg Config, executionID, userID, projectID, stage string, cleanRebuild bool, parentEnv []string) *exec.Cmd {
	args := workerArgs(stage)
	if cfg.PipelineConfigPath != "" {
		args = append(args, "--pipeline-config", cfg.PipelineConfigPath, "--pipeline-private-bindings", cfg.PipelineBindingsPath)
	}
	cmd := exec.Command(cfg.Worker, args...)
	cmd.Dir = cfg.WorkDir
	cmd.Env = workerEnvironment(parentEnv, map[string]string{
		"GCP_PROJECT":           cfg.Project,
		"GOOGLE_CLOUD_PROJECT":  cfg.Project,
		"BUCKET":                cfg.Bucket,
		"FIRESTORE_DATABASE_ID": cfg.Database,
		"LOCAL_CLOUD_SCOPE":     cfg.Scope,
		"USER_ID":               userID,
		"PROJECT_ID":            projectID,
		"EXECUTION_ID":          executionID,
		"CLEAN_REBUILD":         fmt.Sprintf("%t", cleanRebuild),
	})
	cmd.Stdout = io.Discard
	cmd.Stderr = cfg.Stderr
	if cmd.Stderr == nil {
		cmd.Stderr = io.Discard
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}

func newExecutionID() (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "local-" + hex.EncodeToString(raw[:]), nil
}

func workerEnvironment(current []string, overrides map[string]string) []string {
	values := make(map[string]string, len(current)+len(overrides))
	for _, item := range current {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			values[key] = value
		}
	}
	for key, value := range overrides {
		values[key] = value
	}
	result := make([]string, 0, len(values))
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	return result
}

func formatTime(value any) string {
	if timestamp, ok := value.(time.Time); ok && !timestamp.IsZero() {
		return timestamp.UTC().Format(time.RFC3339Nano)
	}
	return ""
}

func formatDuration(start, end any) string {
	started, startOK := start.(time.Time)
	finished, endOK := end.(time.Time)
	if !startOK || !endOK || started.IsZero() || finished.Before(started) {
		return ""
	}
	return finished.Sub(started).Round(time.Millisecond).String()
}
