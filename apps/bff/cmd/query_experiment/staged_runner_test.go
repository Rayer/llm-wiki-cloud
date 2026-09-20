//go:build darwin || linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type stagedMock struct {
	stages          []int
	children        []string
	fail            int
	status, variant string
	badImport       bool
}

func stagedTestRoot(t *testing.T) string {
	t.Helper()
	p, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func stagedTestWrite(t *testing.T, p string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
}
func stagedTestCorpus() stagedTree {
	return stagedTree{"cache/concepts.jsonl": {[]byte("{\"slug\":\"alice\",\"title\":\"Alice\"}\n"), 1234567890000000000}, "wiki/alice.md": {[]byte("Alice follows the White Rabbit."), 1234567890000000000}}
}
func stagedTestExport(t *testing.T, export string) {
	t.Helper()
	stagedTestWrite(t, filepath.Join(export, "index/INDEX.json"), []byte(`{"schema_version":1,"pack":{"id":"fixture","name":"fixture","version":"0","language":["en"],"capabilities":["articles","concepts"]},"articles":[{"id":"01ARZ3NDEKTSV4RRFFQ69G5FAV","entity_id":null,"name":"Alice","path":"articles/alice.md","summary":null,"tags":[],"aliases":[],"confidence":"high"}],"terms":[],"papers":[],"sources":[],"source_concepts":[],"synthesis":[],"stats":{"article_count":1,"draft_count":0,"concept_count":0,"alias_count":0,"knowledge_item_count":0,"source_count":0,"source_segment_count":0,"failed_note_count":0,"failed_concept_count":0}}`))
	stagedTestWrite(t, filepath.Join(export, "articles/alice.md"), []byte("Alice follows the White Rabbit."))
}

func (m *stagedMock) hooks(t *testing.T) stagedHooks {
	t.Helper()
	return stagedHooks{Identity: "explicit-mock", Execution: "explicit_mock_boundaries", Child: func(ctx context.Context, argv, env []string, cwd string) ([]byte, error) {
		m.children = append(m.children, strings.Join(argv[1:], " "))
		if argv[1] == "--version" {
			return []byte("synto, version 0.7.0"), nil
		}
		if argv[1] == "init" {
			return nil, os.MkdirAll(filepath.Join(argv[2], "raw"), 0700)
		}
		if argv[1] == "pack" {
			stagedTestExport(t, argv[len(argv)-1])
			return nil, nil
		}

		if argv[1] == "run" {
			stagedTestWrite(t, filepath.Join(cwd, ".synto/state.db"), []byte("EXPLICIT MOCK"))
			return nil, nil
		}
		if argv[len(argv)-1] == "index" {
			for p, f := range stagedTestCorpus() {
				stagedTestWrite(t, filepath.Join(cwd, p), f.Data)
				stamp := time.Unix(0, f.Mtime)
				if e := os.Chtimes(filepath.Join(cwd, p), stamp, stamp); e != nil {
					t.Fatal(e)
				}
			}
		} else {
			stagedTestWrite(t, filepath.Join(cwd, "cache/suggested_queries.json"), []byte(`{"mock":true}`))
		}
		return nil, nil
	}, Query: func(ctx context.Context, r localRequest, out io.Writer) error {
		switch r.Operation {
		case "validate-config", "validate-profile", "validate":
			b, e := os.ReadFile(r.Config)
			if e != nil || !json.Valid(b) {
				return errors.New("invalid sealed query configuration")
			}
			return json.NewEncoder(out).Encode(map[string]string{})
		case "validate-snapshot":
			files, e := stagedReadTree(r.Snapshot)
			if e != nil {
				return e
			}
			if e = stagedValidateCorpus(files); e != nil {
				return e
			}
			return json.NewEncoder(out).Encode(map[string]string{})
		case "bind-config":
			b, e := os.ReadFile(r.Config)
			if e != nil {
				return e
			}
			var cfg map[string]any
			_ = json.Unmarshal(b, &cfg)
			cfg["project_bindings"] = []map[string]string{{"project_id": r.Project, "generation_id": r.Generation, "profile_id": r.Profile, "prompt_id": r.Prompt}}
			return json.NewEncoder(out).Encode(cfg)
		case "import":
			if m.badImport {
				_ = stagedPutTree(filepath.Join(filepath.Dir(r.Destination), ".import-interrupted"), stagedTree{"partial": {[]byte("x"), 1}})
				return errors.New("declared object unavailable")
			}
			if e := stagedPutTree(r.Destination, stagedTestCorpus()); e != nil {
				return e
			}
			return json.NewEncoder(out).Encode(map[string]any{"ManifestGeneration": 17, "Manifest": map[string]string{"generation_id": "generation-a"}})
		case "suggested-cases":
			return json.NewEncoder(out).Encode([]map[string]string{{"id": "alice", "query": "Alice", "mode": "wiki"}})
		case "query":
			m.stages = append(m.stages, r.Stage)
			v := r.Saved["alice"]
			if r.Stage > 70 && v.Replay.Stage != r.Stage-10 {
				return errors.New("missing mock predecessor")
			}
			v.Replay.Stage = r.Stage
			v.Status = "success"
			if r.Stage == m.fail {
				v.Status = m.status
			}
			if r.Stage == 70 {
				v.Result.AISynth = m.variant
			}
			return json.NewEncoder(out).Encode(map[string]localCaseResult{"alice": v})
		}
		return errors.New("unexpected operation")
	}}
}
func stagedTestOptions(t *testing.T) (string, stagedOptions) {
	t.Helper()
	root := stagedTestRoot(t)
	o, e := stagedParse([]string{"--output", filepath.Join(root, "baseline"), "--raw", filepath.Join(root, "raw"), "--to", "100", "--cases", filepath.Join(root, "cases.jsonl"), "--query-config", filepath.Join(root, "config.json"), "--worker-bin", "/bin/sh", "--synto-bin", "/bin/sh"}, io.Discard)
	if e != nil {
		t.Fatal(e)
	}
	stagedTestWrite(t, filepath.Join(o.Raw, "alice.md"), []byte("public TEST DATA"))
	stagedTestWrite(t, o.Config, []byte(`{"stages":{"query_expander":{"model":"frozen"},"answer_synthesizer":{"reasoning":"none"}}}`))
	stagedTestWrite(t, o.Cases, []byte("{\"id\":\"alice\",\"query\":\"Alice\",\"mode\":\"wiki\"}\n"))
	return root, o
}
func stagedTestRun(t *testing.T, o stagedOptions, m *stagedMock) *stagedReport {
	t.Helper()
	r, e := runStaged(context.Background(), o, m.hooks(t))
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func stagedTestFork(o stagedOptions, name string) stagedOptions {
	o.Fork = o.Output
	o.Output = filepath.Join(filepath.Dir(o.Output), name)
	o.Raw = ""
	o.Snapshot = ""
	o.To = ""
	o.Only = "100"
	return o
}

func TestStagedModesAndDependencyPreflight(t *testing.T) {
	for _, tc := range []struct {
		only, to, from string
		want           []int
	}{{"90,70,80", "", "", []int{70, 80, 90}}, {"", "50", "", []int{10, 20, 50}}, {"", "", "suggested-queries", []int{60, 70, 80, 90, 100}}} {
		got, e := stagedSelection(tc.only, tc.to, tc.from)
		if e != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%v %v", got, e)
		}
	}
	for _, tc := range [][3]string{{"", "", ""}, {"70", "90", ""}, {"70,70", "", ""}, {"30", "", ""}} {
		if _, e := stagedSelection(tc[0], tc[1], tc[2]); e == nil {
			t.Fatal("accepted invalid modes")
		}
	}
	for _, kind := range []string{"mode", "dependency", "config", "link", "endpoint", "nested", "body", "old-fork"} {
		t.Run(kind, func(t *testing.T) {
			root, o := stagedTestOptions(t)
			m := &stagedMock{}
			switch kind {
			case "mode":
				o.Only = "70"
			case "dependency":
				o.Raw = ""
				o.Snapshot = filepath.Join(root, "corpus")
				_ = stagedPutTree(o.Snapshot, stagedTestCorpus())
				o.To = ""
				o.Only = "90"
			case "config":
				stagedTestWrite(t, o.Config, []byte("invalid"))
			case "link":
				if e := os.Symlink(root, filepath.Join(o.Raw, "escape")); e != nil {
					t.Fatal(e)
				}
			case "endpoint":
				o.Settings.URL = "https://example.invalid/?key=secret"
			case "nested":
				o.Output = filepath.Join(o.Raw, "nested")
			case "body":
				o.Raw = ""
				o.Snapshot = filepath.Join(root, "corpus")
				files := stagedTestCorpus()
				delete(files, "wiki/alice.md")
				_ = stagedPutTree(o.Snapshot, files)
				o.To = ""
				o.From = "70"
			case "old-fork":
				o.Raw = ""
				o.Fork = filepath.Join(root, "old")
				_ = stagedPutTree(o.Fork, stagedTree{"run.json": {[]byte(`{"schema":1}`), 1}})
				o.To = ""
				o.Only = "100"
			}
			if _, e := runStaged(context.Background(), o, m.hooks(t)); e == nil {
				t.Fatal("accepted invalid inputs")
			}
			if _, e := os.Stat(o.Output); !errors.Is(e, os.ErrNotExist) {
				t.Fatal("preflight created output")
			}
			if len(m.stages)+len(m.children) != 0 {
				t.Fatal("preflight executed stage")
			}
		})
	}
}
func TestStagedForkIsolationAndCorruption(t *testing.T) {
	_, o := stagedTestOptions(t)
	m := &stagedMock{}
	stagedTestRun(t, o, m)
	if !reflect.DeepEqual(m.stages, []int{70, 80, 90, 100}) {
		t.Fatal(m.stages)
	}
	before, e := stagedReadTree(o.Output)
	if e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"fork-a", "fork-b"} {
		f := stagedTestFork(o, name)
		f.Worker = ""
		f.Synto = "absent"
		fm := &stagedMock{}
		stagedTestRun(t, f, fm)
		if !reflect.DeepEqual(fm.stages, []int{100}) || len(fm.children) != 0 {
			t.Fatal("fork reran upstream")
		}
		wiki := filepath.Join(f.Output, "work/users/test-user/projects/test-project/wiki/alice.md")
		info, _ := os.Stat(wiki)
		if info.ModTime().UnixNano() != before["snapshots/90/wiki/alice.md"].Mtime {
			t.Fatal("mtime changed")
		}
		stagedTestWrite(t, filepath.Join(f.Output, "work/users/test-user/projects/test-project/.synto/state.db"), []byte(name))
	}
	after, _ := stagedReadTree(o.Output)
	if before.digest() != after.digest() || !reflect.DeepEqual(before.mtimes(), after.mtimes()) {
		t.Fatal("donor mutated")
	}
	for _, kind := range []string{"cases", "content", "mtime", "implementation"} {
		t.Run(kind, func(t *testing.T) {
			_, o := stagedTestOptions(t)
			stagedTestRun(t, o, &stagedMock{})
			f := stagedTestFork(o, "bad")
			switch kind {
			case "cases":
				stagedTestWrite(t, o.Cases, []byte("{\"id\":\"alice\",\"query\":\"changed\",\"mode\":\"wiki\"}\n"))
			case "content":
				stagedTestWrite(t, filepath.Join(o.Output, "snapshots/90/wiki/alice.md"), []byte("tampered"))
			case "mtime":
				p := filepath.Join(o.Output, "snapshots/90/wiki/alice.md")
				_ = os.Chtimes(p, time.Now(), time.Now())
			case "implementation":
				p := filepath.Join(o.Output, "run.json")
				b, _ := os.ReadFile(p)
				stagedTestWrite(t, p, []byte(strings.ReplaceAll(string(b), "explicit-mock", "other-binary")))
			}
			if _, e := runStaged(context.Background(), f, (&stagedMock{}).hooks(t)); e == nil {
				t.Fatal("accepted corrupted fork")
			}
			if _, e := os.Stat(f.Output); !errors.Is(e, os.ErrNotExist) {
				t.Fatal("created rejected fork")
			}
		})
	}
}
func TestStagedQueryReplayCompatibilityAndGaps(t *testing.T) {
	_, o := stagedTestOptions(t)
	o.Profile = "existing"
	o.Prompt = "existing-prompt"
	stagedTestRun(t, o, &stagedMock{})
	f := stagedTestFork(o, "bound")
	m := &stagedMock{}
	stagedTestRun(t, f, m)
	if !reflect.DeepEqual(m.stages, []int{100}) {
		t.Fatal(m.stages)
	}
	f = stagedTestFork(o, "gap")
	f.Only = "90,70"
	m = &stagedMock{}
	stagedTestRun(t, f, m)
	if !reflect.DeepEqual(m.stages, []int{70, 90}) {
		t.Fatal(m.stages)
	}
	f = stagedTestFork(o, "changed-gap")
	f.Only = "70,90"
	m = &stagedMock{variant: "changed"}
	r, e := runStaged(context.Background(), f, m.hooks(t))
	if e == nil || !strings.Contains(e.Error(), "requires saved stage 80") || r.Checkpoints[70].Digest == "" || r.Checkpoints[90].Digest != "" {
		t.Fatalf("%v %+v", e, r)
	}
	f = stagedTestFork(o, "synthesis-config")
	stagedTestWrite(t, o.Config, []byte(`{"stages":{"query_expander":{"model":"frozen"},"answer_synthesizer":{"reasoning":"low"}}}`))
	stagedTestRun(t, f, &stagedMock{})
	f = stagedTestFork(o, "bad-expander")
	stagedTestWrite(t, o.Config, []byte(`{"stages":{"query_expander":{"model":"changed"}}}`))
	if _, e := runStaged(context.Background(), f, (&stagedMock{}).hooks(t)); e == nil {
		t.Fatal("accepted changed expansion")
	}
}
func TestStagedNativeForkAndManualCases(t *testing.T) {
	_, o := stagedTestOptions(t)
	stagedTestRun(t, o, &stagedMock{})
	f := stagedTestFork(o, "native")
	f.Only = "20"
	m := &stagedMock{}
	r := stagedTestRun(t, f, m)
	if r.Source.Checkpoint != 10 || len(m.stages) != 0 {
		t.Fatal("wrong native checkpoint")
	}
	f = o
	f.Output = filepath.Join(filepath.Dir(o.Output), "manual")
	f.To = ""
	f.Only = "100,90,80,70,50,20,10"
	m = &stagedMock{}
	stagedTestRun(t, f, m)
	for _, c := range m.children {
		if strings.HasSuffix(c, "suggested") {
			t.Fatal("manual cases executed 60")
		}
	}
}
func TestStagedCloudAndQueryOnlyNeedNoSynto(t *testing.T) {
	for _, cloud := range []bool{false, true} {
		root, o := stagedTestOptions(t)
		o.Raw = ""
		o.To = ""
		o.From = "70"
		o.Synto = "absent"
		o.Worker = ""
		o.Snapshot = filepath.Join(root, "snapshot")
		if cloud {
			o.From = "60"
			o.Worker = "/bin/sh"
			o.Snapshot = "gs://llm-wiki-data-dev/users/u/projects/p"
		} else {
			corpus := stagedTestCorpus()
			corpus["cache/id_map.json"] = stagedFile{[]byte(`{"concept":{"abcdef123456":"alice"}}`), 1234567890000000000}
			if e := stagedPutTree(o.Snapshot, corpus); e != nil {
				t.Fatal(e)
			}
		}
		m := &stagedMock{fail: 100, status: "no_grounded_answer"}
		r := stagedTestRun(t, o, m)
		if r.Status != "success" || r.Quality.Status != "not_requested" || r.Implementation["synto_implementation"] != "" {
			t.Fatal(r)
		}
		for _, c := range m.children {
			if strings.Contains(c, "--version") || strings.Contains(c, "init") || strings.HasSuffix(c, "index") {
				t.Fatal("cloud probed upstream")
			}
		}
	}
	_, o := stagedTestOptions(t)
	o.Raw = ""
	o.To = ""
	o.From = "60"
	o.Snapshot = "gs://llm-wiki-data-dev/users/u/projects/p"
	m := &stagedMock{badImport: true}
	r, e := runStaged(context.Background(), o, m.hooks(t))
	if e == nil || r.Status != "failure" || len(m.stages) != 0 {
		t.Fatal("failed import accepted")
	}
	matches, _ := filepath.Glob(filepath.Join(o.Output, ".import-*"))
	if len(matches) != 0 {
		t.Fatal("partial import retained")
	}
}
func TestStagedNegativeFailuresRetainHonestRecords(t *testing.T) {
	for _, status := range []string{"no_grounded_answer", "partial_expansion", "execution_failure"} {
		t.Run(status, func(t *testing.T) {
			_, o := stagedTestOptions(t)
			o.Grounded = true
			m := &stagedMock{fail: 100, status: status}
			if status == "partial_expansion" {
				m.fail = 70
			}
			r, e := runStaged(context.Background(), o, m.hooks(t))
			if status == "no_grounded_answer" {
				if e != nil || r.Status != "success" || r.Quality.Status != "failed" || r.Checkpoints[100].Digest == "" {
					t.Fatalf("%v %+v", e, r)
				}
			} else {
				if e == nil || r.Status != "partial" || r.Checkpoints[m.fail].Digest != "" {
					t.Fatalf("%v %+v", e, r)
				}
			}
			if _, e := os.Stat(filepath.Join(o.Output, "artifacts", strconv.Itoa(m.fail)+".json")); e != nil {
				t.Fatal(e)
			}
			var persisted stagedReport
			b, _ := os.ReadFile(filepath.Join(o.Output, "run.json"))
			if json.Unmarshal(b, &persisted) != nil || persisted.Status != r.Status {
				t.Fatal("missing atomic receipt")
			}
		})
	}
}
func TestStagedChildDiagnosticsAndCancellation(t *testing.T) {
	root := stagedTestRoot(t)
	_, e := stagedChild(context.Background(), []string{"/bin/sh", "-c", `echo '{"error_type":"suggested_candidate_cardinality_invalid"}'; echo private-provider-body >&2; exit 7`}, []string{"PATH=/usr/bin:/bin"}, root)
	if e == nil || !strings.Contains(e.Error(), "exit 7; suggested_candidate_cardinality_invalid") || strings.Contains(e.Error(), "private-provider-body") {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, e = stagedChild(ctx, []string{"/bin/sh", "-c", `trap '' TERM; (trap '' TERM; sleep 20) & echo $! > child.pid; wait`}, []string{"PATH=/usr/bin:/bin"}, root)
	if e == nil || time.Since(start) > 3*time.Second {
		t.Fatal("timeout failed", e)
	}
	b, _ := os.ReadFile(filepath.Join(root, "child.pid"))
	var pid int
	_, _ = fmt.Sscan(string(b), &pid)
	if pid == 0 {
		t.Fatal("missing child pid")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("descendant survived cancellation")
}
func TestStagedEnvironmentAllowlist(t *testing.T) {
	t.Setenv("UNRELATED_SECRET", "must-not-leak")
	t.Setenv("DEEPSEEK_API_KEY", "test-key")
	env := strings.Join(stagedEnvironment("/private/run", false, false), "\n")
	if strings.Contains(env, "test-key") || strings.Contains(env, "must-not-leak") || !strings.Contains(env, "HOME=/private/run/home") {
		t.Fatal("unsafe non-inference environment")
	}
	env = strings.Join(stagedEnvironment("/private/run", true, false), "\n")
	if !strings.Contains(env, "test-key") || strings.Contains(env, "must-not-leak") {
		t.Fatal("unsafe inference environment")
	}
}

func TestStagedProducerOutputValidation(t *testing.T) {
	for _, kind := range []string{"valid", "invalid-index", "no-output", "missing-body", "empty-body", "changed-raw", "changed-mtime", "reported-failure", "partial-output"} {
		t.Run(kind, func(t *testing.T) {
			root := stagedTestRoot(t)
			before := stagedTree{"raw/a.md": {[]byte("raw immutable"), 1234567890000000000}}
			vault, export := filepath.Join(root, "vault"), filepath.Join(root, "export")
			if e := stagedPutTree(vault, before); e != nil {
				t.Fatal(e)
			}
			stagedTestExport(t, export)
			indexPath := filepath.Join(export, "index/INDEX.json")
			switch kind {
			case "invalid-index":
				stagedTestWrite(t, indexPath, []byte(`{}`))
			case "no-output":
				b, _ := os.ReadFile(indexPath)
				var v map[string]any
				_ = json.Unmarshal(b, &v)
				v["articles"] = []any{}
				stagedTestWrite(t, indexPath, stagedJSON(v))
			case "missing-body":
				if e := os.Remove(filepath.Join(export, "articles/alice.md")); e != nil {
					t.Fatal(e)
				}
			case "empty-body":
				stagedTestWrite(t, filepath.Join(export, "articles/alice.md"), []byte(" \n"))
			case "changed-raw":
				stagedTestWrite(t, filepath.Join(vault, "raw/a.md"), []byte("changed"))
			case "changed-mtime":
				if e := os.Chtimes(filepath.Join(vault, "raw/a.md"), time.Now(), time.Now()); e != nil {
					t.Fatal(e)
				}
			case "reported-failure":
				// Reject failures actually reported by the validated public export.
				b, _ := os.ReadFile(indexPath)
				stagedTestWrite(t, indexPath, []byte(strings.Replace(string(b), `"failed_note_count":0`, `"failed_note_count":1`, 1)))
			}
			if kind == "partial-output" {
				// A raw input absent from the export is not proof of a failure or success.
				before["raw/unrepresented.md"] = stagedFile{[]byte("another input"), 1234567890000000000}
				if e := os.RemoveAll(vault); e != nil {
					t.Fatal(e)
				}
				if e := stagedPutTree(vault, before); e != nil {
					t.Fatal(e)
				}
			}
			e := stagedValidateProducer(vault, export, before)
			if (e == nil) != (kind == "valid" || kind == "partial-output") {
				t.Fatalf("%s: %v", kind, e)
			}
		})
	}
}

func TestStagedProducerExportFailure(t *testing.T) {
	for _, kind := range []string{"run-exit", "export-exit", "invalid-export"} {
		t.Run(kind, func(t *testing.T) {
			_, o := stagedTestOptions(t)
			o.To = "20"
			m := &stagedMock{}
			h := m.hooks(t)
			child := h.Child
			h.Child = func(ctx context.Context, argv, env []string, cwd string) ([]byte, error) {
				if kind == "run-exit" && argv[1] == "run" || kind == "export-exit" && argv[1] == "pack" {
					return nil, errors.New("explicit child failure")
				}
				if kind == "invalid-export" && argv[1] == "pack" {
					return nil, nil
				}
				return child(ctx, argv, env, cwd)
			}
			r, err := runStaged(context.Background(), o, h)
			if err == nil || r == nil || r.Status != "partial" || r.Failure == "" || r.Stages[len(r.Stages)-1].Status != "failure" {
				t.Fatalf("failure not recorded: %+v %v", r, err)
			}
			if _, ok := r.Checkpoints[20]; ok {
				t.Fatal("failed producer checkpoint accepted")
			}
		})
	}
}

func TestStagedSyntoLauncherIdentity(t *testing.T) {
	root := stagedTestRoot(t)
	launcher := filepath.Join(root, "synto")
	stagedTestWrite(t, launcher, []byte("#!/arbitrary/python\nprint('synto')\n"))
	first, err := stagedSyntoIdentity(launcher)
	if err != nil {
		t.Fatal(err)
	}
	stagedTestWrite(t, filepath.Join(root, "unrelated.py"), []byte("unrelated"))
	same, err := stagedSyntoIdentity(launcher)
	if err != nil || same != first {
		t.Fatal("identity depends on installation layout")
	}
	stagedTestWrite(t, launcher, []byte("#!/arbitrary/python\nprint('changed')\n"))
	changed, err := stagedSyntoIdentity(launcher)
	if err != nil || changed == first {
		t.Fatal("launcher change not detected")
	}
}

func TestStagedPublicSyntoInitialization(t *testing.T) {
	synto, e := exec.LookPath("synto")
	if e != nil {
		t.Skip("public synto not installed")
	}
	root, o := stagedTestOptions(t)
	o.To = "10"
	o.Synto = synto
	o.Worker = ""
	o.Config = filepath.Join(root, "config.json")
	m := &stagedMock{}
	h := m.hooks(t)
	h.Child = stagedChild
	before, e := stagedReadTree(o.Raw)
	if e != nil {
		t.Fatal(e)
	}
	r, e := runStaged(context.Background(), o, h)
	if e != nil {
		t.Fatal(e)
	}
	if r.Checkpoints[10].Digest == "" {
		t.Fatal("missing source checkpoint")
	}
	after, _ := stagedReadTree(o.Raw)
	if before.digest() != after.digest() || !reflect.DeepEqual(before.mtimes(), after.mtimes()) {
		t.Fatal("source mutated")
	}
	vault := filepath.Join(o.Output, "work", r.Logical)
	if _, e = os.Stat(filepath.Join(vault, ".synto/state.db")); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("init created DB")
	}
	if _, e = os.Stat(filepath.Join(vault, ".git/refs/heads/master")); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("unexpected init commit")
	}
	home, _ := stagedReadTree(filepath.Join(o.Output, "home"))
	xdg, _ := stagedReadTree(filepath.Join(o.Output, "xdg"))
	if len(home)+len(xdg) != 0 {
		t.Fatal("initialization wrote private home or settings")
	}
}

func TestStagedRunnerReusesProductionExecutorAndForksWithoutExpansion(t *testing.T) {
	root, o := stagedTestOptions(t)
	o.Raw = ""
	o.To = ""
	o.From = "70"
	o.Worker = ""
	o.Synto = "absent"
	o.Snapshot = filepath.Join(root, "snapshot")
	corpus := stagedTestCorpus()
	corpus["cache/id_map.json"] = stagedFile{[]byte(`{"concept":{"abcdef123456":"alice"}}`), 1234567890000000000}
	if e := stagedPutTree(o.Snapshot, corpus); e != nil {
		t.Fatal(e)
	}
	fixture, e := filepath.Abs("../../../../scripts/local_e2e/fixtures/query-dns.json")
	if e != nil {
		t.Fatal(e)
	}
	o.Config = fixture
	o.Profile = "corpus-derived-tech-document-v1"
	o.Prompt = "domain-neutral-technical-v1"
	o.Grounded = true
	transport := &platformTransport{t: t}
	old := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = old })
	t.Setenv("DEEPSEEK_API_KEY", "explicit-mock-key")
	h := stagedDefaults()
	h.Execution = "explicit_mock_http_transport"
	h.Child = func(context.Context, []string, []string, string) ([]byte, error) {
		t.Fatal("query orchestration started child")
		return nil, nil
	}
	r, e := runStaged(context.Background(), o, h)
	if e != nil || r.Quality.Status != "passed" {
		t.Fatalf("%v %+v", e, r)
	}
	artifact := r.Checkpoints[100].Artifacts[100].Data["alice"]
	if artifact.Identity == nil || artifact.Identity.ProfileID != o.Profile || artifact.Result.AISynth == "" || len(artifact.Result.Citations) != 1 || artifact.Result.Citations[0].Slug != "alice" {
		t.Fatalf("missing production proof: %+v", artifact)
	}
	f := stagedTestFork(o, "production-fork")
	f.From = ""
	r, e = runStaged(context.Background(), f, h)
	if e != nil || r.Quality.Status != "passed" {
		t.Fatalf("%v %+v", e, r)
	}
	if transport.expansion.Load() != 3 || transport.synthesis.Load() != 2 {
		t.Fatal("fork replay reran expansion or skipped synthesis")
	}
	files, e := stagedReadTree(o.Output)
	if e != nil {
		t.Fatal(e)
	}
	for path, file := range files {
		if strings.Contains(string(file.Data), "explicit-mock-key") {
			t.Fatalf("secret persisted in %s", path)
		}
	}
}

func TestStagedCancellationRetainsOnlyCompletedCheckpoint(t *testing.T) {
	_, o := stagedTestOptions(t)
	o.To = "20"
	o.Timeout = 1
	m := &stagedMock{}
	h := m.hooks(t)
	child := h.Child
	h.Child = func(ctx context.Context, argv, env []string, cwd string) ([]byte, error) {
		if argv[1] == "run" {
			<-ctx.Done()
			return nil, errors.New("child interrupted or exceeded --timeout; partial workspace retained")
		}
		return child(ctx, argv, env, cwd)
	}
	r, e := runStaged(context.Background(), o, h)
	if e == nil || r.Status != "partial" || r.Checkpoints[10].Digest == "" || r.Checkpoints[20].Digest != "" {
		t.Fatalf("%v %+v", e, r)
	}
	var persisted stagedReport
	b, e := os.ReadFile(filepath.Join(o.Output, "run.json"))
	if e != nil || json.Unmarshal(b, &persisted) != nil || persisted.Status != "partial" || persisted.Stages[1].Status != "failure" {
		t.Fatal("missing interruption receipt")
	}
}
func TestStagedSuggestedCasesAndArtifactDependencyCorruption(t *testing.T) {
	_, o := stagedTestOptions(t)
	o.Cases = ""
	o.Suggested = true
	stagedTestRun(t, o, &stagedMock{})
	f := stagedTestFork(o, "suggested")
	stagedTestRun(t, f, &stagedMock{})
	p := filepath.Join(o.Output, "run.json")
	data, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	var r stagedReport
	if e = json.Unmarshal(data, &r); e != nil {
		t.Fatal(e)
	}
	a := r.Checkpoints[100].Artifacts[80]
	a.Predecessor = "corrupt"
	r.Checkpoints[100].Artifacts[80] = a
	if e = stagedAtomic(p, r); e != nil {
		t.Fatal(e)
	}
	f = stagedTestFork(o, "bad-dependency")
	m := &stagedMock{}
	if _, e = runStaged(context.Background(), f, m.hooks(t)); e == nil || !strings.Contains(e.Error(), "dependency digest mismatch") || len(m.stages) != 0 {
		t.Fatal("corrupt saved dependency executed", e)
	}
}
func TestStagedSnapshotBoundsAndSpecialFiles(t *testing.T) {
	root := stagedTestRoot(t)
	if e := syscall.Mkfifo(filepath.Join(root, "fifo"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := stagedReadTree(root); e == nil {
		t.Fatal("accepted FIFO")
	}
	if e := os.Remove(filepath.Join(root, "fifo")); e != nil {
		t.Fatal(e)
	}
	f, e := os.Create(filepath.Join(root, "huge"))
	if e != nil {
		t.Fatal(e)
	}
	if e = f.Truncate(stagedMaxBytes + 1); e != nil {
		t.Fatal(e)
	}
	f.Close()
	if _, e := stagedReadTree(root); e == nil {
		t.Fatal("accepted oversized file")
	}
}

func TestStagedFreshNativeRejectsDatabaseAndSidecarsBeforeOutput(t *testing.T) {
	for _, path := range []string{".synto/state.db", ".synto/state.db-wal", ".synto/state.db-shm"} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			root, o := stagedTestOptions(t)
			o.Raw = ""
			o.To = ""
			o.Only = "20"
			o.Snapshot = filepath.Join(root, "prepared")
			files := stagedTree{"raw/a.md": {[]byte("raw"), 1}, "synto.toml": {o.Settings.toml(), 1}, path: {[]byte("old state"), 1}}
			if e := stagedPutTree(o.Snapshot, files); e != nil {
				t.Fatal(e)
			}
			m := &stagedMock{}
			if _, e := runStaged(context.Background(), o, m.hooks(t)); e == nil || !strings.Contains(e.Error(), "existing state DB") {
				t.Fatal(e)
			}
			if _, e := os.Stat(o.Output); !errors.Is(e, os.ErrNotExist) || len(m.children) != 0 {
				t.Fatal("rejected state created output or executed")
			}
		})
	}
}

func TestStagedMissingInferenceKeyFailsBeforeOutput(t *testing.T) {
	_, o := stagedTestOptions(t)
	t.Setenv("DEEPSEEK_API_KEY", "")
	if _, e := runStaged(context.Background(), o, stagedDefaults()); e == nil || !strings.Contains(e.Error(), "require DEEPSEEK_API_KEY") {
		t.Fatal(e)
	}
	if _, e := os.Stat(o.Output); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("missing key created output")
	}
}

func TestStagedNativeSnapshotBindsArtifactsAndImplementation(t *testing.T) {
	_, o := stagedTestOptions(t)
	o.To = "20"
	stagedTestRun(t, o, &stagedMock{})
	snapshot := filepath.Join(o.Output, "snapshots/20")
	files, e := stagedReadTree(snapshot)
	if e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"valid", "database", "implementation"} {
		t.Run(kind, func(t *testing.T) {
			copy := stagedTree{}
			for p, f := range files {
				copy[p] = f
			}
			if kind == "database" {
				copy[".synto/state.db"] = stagedFile{[]byte("tampered DB"), 1}
			}
			if kind == "implementation" {
				var receipt stagedNativeReceipt
				_ = json.Unmarshal(copy[".synto/local-run.json"].Data, &receipt)
				receipt.Implementation = "different-synto"
				copy[".synto/local-run.json"] = stagedFile{stagedJSON(receipt), 1}
			}
			f := o
			f.Raw = ""
			f.To = ""
			f.Only = "50"
			f.Snapshot = filepath.Join(filepath.Dir(o.Output), "native-"+kind)
			f.Output = filepath.Join(filepath.Dir(o.Output), "index-"+kind)
			if e := stagedPutTree(f.Snapshot, copy); e != nil {
				t.Fatal(e)
			}
			m := &stagedMock{}
			_, e := runStaged(context.Background(), f, m.hooks(t))
			if (e == nil) != (kind == "valid") {
				t.Fatal(kind, e)
			}
			if kind != "valid" {
				if _, e := os.Stat(f.Output); !errors.Is(e, os.ErrNotExist) || len(m.children) != 0 {
					t.Fatal("invalid native snapshot executed")
				}
			}
		})
	}
}
