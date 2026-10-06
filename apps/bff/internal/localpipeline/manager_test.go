package localpipeline

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	cloudstorage "cloud.google.com/go/storage"
	"github.com/rayer/llm-wiki-bff/internal/generation"
	"github.com/rayer/llm-wiki-bff/internal/localcloud"
)

func TestWorkerArgsStayStructuredAtNativeProcessBoundary(t *testing.T) {
	full := workerArgs("full")
	if len(full) != 2 || full[0] != "run" || full[1] != `[["run","--auto-approve"]]` {
		t.Fatalf("full worker args=%q", full)
	}
	if strings.Contains(strings.Join(full, " "), ";") || strings.Contains(strings.Join(full, " "), "$(") {
		t.Fatalf("worker args contain shell syntax: %q", full)
	}
	if got := workerArgs("suggested-queries"); len(got) != 1 || got[0] != "suggested-queries" {
		t.Fatalf("suggested query worker args=%q", got)
	}
}

func TestNativeWorkerCommandCrossesRealProcessBoundaryWithScopedEnvironment(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable for executable process fixture")
	}
	tmp := t.TempDir()
	worker := filepath.Join(tmp, "worker-fixture")
	output := filepath.Join(tmp, "boundary.json")
	script := "#!" + python + "\nimport json,os,sys\njson.dump({'args':sys.argv[1:],'cwd':os.getcwd(),'env':{k:os.environ.get(k) for k in ['GCP_PROJECT','GOOGLE_CLOUD_PROJECT','BUCKET','FIRESTORE_DATABASE_ID','LOCAL_CLOUD_SCOPE','USER_ID','PROJECT_ID','EXECUTION_ID','CLEAN_REBUILD','KEEP_FROM_BFF']}},open(os.environ['PROBE_OUTPUT'],'w'))\n"
	if err := os.WriteFile(worker, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := newWorkerCommand(Config{Worker: worker, WorkDir: tmp, Project: "llm-wiki-cloud", Bucket: "llm-wiki-cloud-local", Database: "llm-wiki-cloud-local", Scope: "worktree-one"}, "local-execution-1", "user-a", "project-a", "full", false, []string{"PATH=" + os.Getenv("PATH"), "KEEP_FROM_BFF=preserved"})
	cmd.Env = append(cmd.Env, "PROBE_OUTPUT="+output)
	if err := cmd.Run(); err != nil {
		t.Fatalf("run native worker fixture: %v", err)
	}
	var observed struct {
		Args []string          `json:"args"`
		CWD  string            `json:"cwd"`
		Env  map[string]string `json:"env"`
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &observed); err != nil {
		t.Fatal(err)
	}
	resolvedWorkDir, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"run", `[["run","--auto-approve"]]`}
	if strings.Join(observed.Args, "\x00") != strings.Join(wantArgs, "\x00") || observed.CWD != resolvedWorkDir {
		t.Fatalf("worker process args=%q cwd=%q want_cwd=%q", observed.Args, observed.CWD, resolvedWorkDir)
	}
	wantEnv := map[string]string{
		"GCP_PROJECT": "llm-wiki-cloud", "GOOGLE_CLOUD_PROJECT": "llm-wiki-cloud",
		"BUCKET": "llm-wiki-cloud-local", "FIRESTORE_DATABASE_ID": "llm-wiki-cloud-local",
		"LOCAL_CLOUD_SCOPE": "worktree-one", "USER_ID": "user-a", "PROJECT_ID": "project-a",
		"EXECUTION_ID": "local-execution-1", "CLEAN_REBUILD": "false", "KEEP_FROM_BFF": "preserved",
	}
	for key, value := range wantEnv {
		if observed.Env[key] != value {
			t.Errorf("worker environment %s=%q, want %q", key, observed.Env[key], value)
		}
	}
}

func TestCloseTimeoutKillsEveryManagedGroupAndLeavesOtherGroupAlone(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable for process-group fixture")
	}
	start := func() *process {
		cmd := exec.Command(python, "-c", "import signal,time; signal.signal(signal.SIGTERM, signal.SIG_IGN); print('ready',flush=True); time.sleep(60)")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		line, err := bufio.NewReader(stdout).ReadString('\n')
		if err != nil || line != "ready\n" {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
			t.Fatalf("process readiness=%q err=%v", line, err)
		}
		p := &process{cmd: cmd, done: make(chan struct{})}
		go func() { _ = cmd.Wait(); close(p.done) }()
		return p
	}
	one, two, unrelated := start(), start(), start()
	manager := &Manager{processes: map[string]*process{"one": one, "two": two}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := manager.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close() = %v, want context canceled", err)
	}
	for label, managed := range map[string]*process{"one": one, "two": two} {
		select {
		case <-managed.done:
		case <-time.After(3 * time.Second):
			t.Errorf("managed process group %s was not reaped", label)
		}
		if err := syscall.Kill(-managed.cmd.Process.Pid, 0); !errors.Is(err, syscall.ESRCH) {
			t.Errorf("managed process group %s still exists (kill error=%v)", label, err)
		}
	}
	if err := syscall.Kill(-unrelated.cmd.Process.Pid, 0); err != nil {
		t.Fatalf("unrelated process group was touched: %v", err)
	}
	_ = syscall.Kill(-unrelated.cmd.Process.Pid, syscall.SIGKILL)
	select {
	case <-unrelated.done:
	case <-time.After(3 * time.Second):
		t.Fatal("unrelated fixture cleanup failed")
	}
}

func TestCloseCannotSnapshotBetweenSpawnAndRegistration(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable for process-group fixture")
	}
	startChild := func(code string) *exec.Cmd {
		cmd := exec.Command(python, "-c", code)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
			_ = cmd.Wait()
		})
		return cmd
	}

	manager := &Manager{processes: make(map[string]*process)}
	cmd := exec.Command(python, "-c", "import signal,time; signal.signal(signal.SIGTERM, signal.SIG_IGN); print('ready',flush=True); time.sleep(60)")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	p := &process{cmd: cmd, done: make(chan struct{})}
	spawned, allowRegistration := make(chan struct{}), make(chan struct{})
	startErr := make(chan error, 1)
	go func() {
		err := manager.startProcess("execution", p, func() error {
			if err := cmd.Start(); err != nil {
				return err
			}
			line, err := bufio.NewReader(stdout).ReadString('\n')
			if err != nil || line != "ready\n" {
				return errors.New("fixture process did not become ready")
			}
			close(spawned)
			<-allowRegistration
			return nil
		})
		if err == nil {
			go func() {
				_ = cmd.Wait()
				manager.mu.Lock()
				delete(manager.processes, "execution")
				manager.mu.Unlock()
				close(p.done)
			}()
		}
		startErr <- err
	}()
	<-spawned

	unrelated := startChild("import time; time.sleep(60)")
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(allowRegistration)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	closeErr := manager.Close(ctx)
	if err := <-startErr; err != nil {
		t.Fatal(err)
	}
	if !errors.Is(closeErr, context.DeadlineExceeded) {
		t.Fatalf("Close() = %v, want timeout after killing registered child", closeErr)
	}
	select {
	case <-p.done:
	case <-time.After(3 * time.Second):
		t.Fatal("registered child was not reaped after Close")
	}
	if err := syscall.Kill(-cmd.Process.Pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("registered child group remains alive: %v", err)
	}
	if err := syscall.Kill(-unrelated.Process.Pid, 0); err != nil {
		t.Fatalf("unrelated process group was touched: %v", err)
	}
	if err := manager.startProcess("late", &process{cmd: exec.Command(python, "-c", "pass")}, func() error { return nil }); !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("start after Close = %v, want ErrManagerClosed", err)
	}
}

func TestCloseGracefullyRacesWithWorkerFinish(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable for process-group fixture")
	}
	cmd := exec.Command(python, "-c", "import signal,sys,time; signal.signal(signal.SIGTERM, lambda *_: sys.exit(0)); print('ready',flush=True); time.sleep(60)")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	p := &process{cmd: cmd, done: make(chan struct{})}
	manager := &Manager{processes: make(map[string]*process)}
	if err := manager.startProcess("execution", p, cmd.Start); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "ready\n" {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		t.Fatalf("fixture process readiness=%q err=%v", line, err)
	}
	go func() {
		_ = cmd.Wait()
		manager.mu.Lock()
		delete(manager.processes, "execution")
		manager.mu.Unlock()
		close(p.done)
	}()
	t.Cleanup(func() {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err == nil {
			_ = cmd.Wait()
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := manager.Close(ctx); err != nil {
		t.Fatalf("Close() while worker finished = %v", err)
	}
	select {
	case <-p.done:
	case <-time.After(time.Second):
		t.Fatal("worker finish was not observed")
	}
	if err := syscall.Kill(-cmd.Process.Pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("finished process group remains alive: %v", err)
	}
}

func TestPublicationOutcomeUsesExecutionBoundCommitWhenReceiptIsMissing(t *testing.T) {
	otherCommit := generation.Manifest{GenerationID: "generation-b", LocalExecutionID: "local-execution-b"}
	otherPublication, _ := ResolvePublicationOutcome("local-execution-a", "generation-before", otherCommit, true, true, nil, nil, nil, cloudstorage.ErrObjectNotExist)
	if otherPublication != "UNKNOWN" {
		t.Fatalf("unrelated current generation outcome=%q; want UNKNOWN", otherPublication)
	}

	receipt, err := localcloud.EncodePublicationReceipt(localcloud.PublicationReceipt{
		ExecutionID: "local-execution-a", GenerationID: "generation-a", ManifestGeneration: 42,
	})
	if err != nil {
		t.Fatal(err)
	}
	committedAfterChildError := errors.New("receipt cleanup failed")
	state, reason := ResolvePublicationOutcome("local-execution-a", "generation-before", generation.Manifest{}, false, false, committedAfterChildError, errors.New("current read raced"), receipt, nil)
	if state != "SUCCEEDED" || !strings.Contains(reason, "afterward") {
		t.Fatalf("matching post-commit receipt outcome=%q reason=%q", state, reason)
	}

	ownCommit := generation.Manifest{GenerationID: "generation-a", LocalExecutionID: "local-execution-a"}
	state, reason = ResolvePublicationOutcome("local-execution-a", "generation-before", ownCommit, true, false, committedAfterChildError, nil, nil, cloudstorage.ErrObjectNotExist)
	if state != "SUCCEEDED" || !strings.Contains(reason, "committed its manifest") {
		t.Fatalf("own commit without receipt outcome=%q reason=%q; want SUCCEEDED", state, reason)
	}

	state, _ = ResolvePublicationOutcome("local-execution-a", "generation-before", generation.Manifest{GenerationID: "generation-before"}, true, true, nil, nil, nil, cloudstorage.ErrObjectNotExist)
	if state != "FAILED" {
		t.Fatalf("missing own receipt without publication outcome=%q; want FAILED", state)
	}
}
