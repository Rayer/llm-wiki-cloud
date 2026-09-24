//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const stagedSchema = "lwc-local-go-v2"

func stagedMain(args []string) int {
	o, err := stagedParse(args, os.Stderr)
	if err == flag.ErrHelp {
		return 0
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var report *stagedReport
	if err == nil {
		report, err = runStaged(ctx, o, stagedDefaults())
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Fprintln(os.Stdout, string(stagedJSON(map[string]any{"status": report.Status, "execution": report.Execution, "quality": report.Quality})))
	if report.Quality.Status == "failed" {
		return 2
	}
	return 0
}

var stagedOrdinals = []int{10, 20, 50, 60, 70, 80, 90, 100}
var stagedNames = map[int]string{10: "source", 20: "synto-run", 50: "index", 60: "suggested-queries", 70: "expansion", 80: "matching", 90: "selection", 100: "synthesis"}
var stagedComponent = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type stagedOptions struct {
	ExperimentRoot, LauncherMetadata                                                                          string
	Output, Raw, Snapshot, Fork, Only, To, From, User, Project, Cases, Config, Profile, Prompt, Worker, Synto string
	Suggested, Grounded                                                                                       bool
	Timeout                                                                                                   int
	Settings                                                                                                  stagedSyntoConfig
}
type stagedArtifact struct {
	Corpus      string                     `json:"corpus"`
	Config      string                     `json:"config"`
	Cases       string                     `json:"cases"`
	Predecessor string                     `json:"predecessor"`
	Data        map[string]localCaseResult `json:"data"`
}
type stagedCheckpoint struct {
	Digest    string                 `json:"snapshot_digest"`
	Mtimes    map[string]int64       `json:"mtimes"`
	Artifacts map[int]stagedArtifact `json:"query_artifacts"`
}
type stagedSource struct {
	Kind        string          `json:"kind"`
	Digest      string          `json:"snapshot_digest,omitempty"`
	RawDigest   string          `json:"raw_digest,omitempty"`
	MtimeDigest string          `json:"mtime_digest,omitempty"`
	Checkpoint  int             `json:"checkpoint,omitempty"`
	Origin      *stagedSource   `json:"origin,omitempty"`
	Published   json.RawMessage `json:"published,omitempty"`
	LocatorHash string          `json:"locator_sha256,omitempty"`
	MtimePolicy string          `json:"mtime_policy,omitempty"`
}
type stagedRecord struct {
	Ordinal   int               `json:"ordinal"`
	Name      string            `json:"name"`
	Status    string            `json:"status"`
	Execution string            `json:"execution"`
	Duration  float64           `json:"duration_seconds"`
	Cases     map[string]string `json:"case_statuses,omitempty"`
}
type stagedQuality struct {
	Oracle string `json:"oracle"`
	Status string `json:"status"`
}
type stagedReport struct {
	Environment        *stagedEnvironmentReceipt `json:"environment,omitempty"`
	Schema             string                    `json:"schema"`
	Status             string                    `json:"status"`
	Implementation     map[string]string         `json:"implementation"`
	Source             stagedSource              `json:"source"`
	Settings           stagedSyntoConfig         `json:"synto_config"`
	ConfigDigest       string                    `json:"query_config_digest"`
	SourceConfigDigest string                    `json:"source_query_config_digest"`
	Logical            string                    `json:"logical_project"`
	Selected           []int                     `json:"selected"`
	Stages             []*stagedRecord           `json:"stages"`
	Checkpoints        map[int]stagedCheckpoint  `json:"checkpoints"`
	Quality            stagedQuality             `json:"quality"`
	Execution          string                    `json:"execution"`
	Failure            string                    `json:"failure,omitempty"`
}

// Tests replace only explicit boundaries. Production always calls the existing
// handler directly, which composes queryruntime.NewExecutor.
type stagedHooks struct {
	Query     func(context.Context, localRequest, io.Writer) error
	Child     func(context.Context, []string, []string, string) ([]byte, error)
	Identity  string
	Execution string
}

func stagedDefaults() stagedHooks {
	return stagedHooks{Query: executeLocalPlatform, Child: stagedChild, Execution: "live_providers"}
}
func stagedParse(args []string, out io.Writer) (stagedOptions, error) {
	var o stagedOptions
	f := flag.NewFlagSet("query_experiment staged", flag.ContinueOnError)
	f.SetOutput(out)
	f.StringVar(&o.ExperimentRoot, "experiment-root", "", "existing mounted experiment directory; all data paths must be children")
	f.StringVar(&o.LauncherMetadata, "launcher-metadata", "", "JSON metadata from external image inspection (required in experiment image)")
	f.StringVar(&o.Output, "output", "", "new isolated run directory")
	f.StringVar(&o.Raw, "raw", "", "canonical raw Markdown directory")
	f.StringVar(&o.Snapshot, "input-snapshot", "", "canonical local snapshot or DEV gs:// Project locator")
	f.StringVar(&o.Fork, "fork", "", "compatible Go run to fork")
	f.StringVar(&o.Only, "only", "", "comma-separated stage ordinals or names")
	f.StringVar(&o.To, "to", "", "last stage, inclusive")
	f.StringVar(&o.From, "from", "", "first stage, inclusive")
	f.StringVar(&o.User, "user", "test-user", "local Project user")
	f.StringVar(&o.Project, "project", "test-project", "local Project ID")
	f.StringVar(&o.Cases, "cases", "", "strict JSONL cases")
	f.BoolVar(&o.Suggested, "suggested-cases", false, "derive cases from suggested queries")
	f.StringVar(&o.Config, "query-config", "", "sealed production query configuration")
	f.StringVar(&o.Profile, "query-profile", "", "existing profile ID")
	f.StringVar(&o.Prompt, "query-prompt", "", "existing prompt ID")
	f.StringVar(&o.Worker, "worker-bin", "", "worker executable, needed only for 50/60")
	f.StringVar(&o.Synto, "synto-bin", "synto", "public Synto executable, needed only for 10/20/50")
	f.IntVar(&o.Timeout, "timeout", 600, "seconds per stage/child (1..3600)")
	f.BoolVar(&o.Grounded, "require-grounded", false, "require answer and resolvable citation inventory for every case")
	env := func(k, d string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return d
	}
	f.StringVar(&o.Settings.Provider, "synto-provider", env("LWC_SYNTO_PROVIDER_NAME", "custom"), "Synto provider")
	f.StringVar(&o.Settings.URL, "synto-url", env("LWC_SYNTO_PROVIDER_URL", "https://api.deepseek.com/v1"), "credential-free HTTPS endpoint")
	f.StringVar(&o.Settings.Fast, "synto-fast", env("LWC_SYNTO_FAST_MODEL", "deepseek-flash"), "fast model")
	f.StringVar(&o.Settings.Heavy, "synto-heavy", env("LWC_SYNTO_HEAVY_MODEL", "deepseek-flash"), "heavy model")
	if err := f.Parse(args); err != nil {
		return o, err
	}
	if f.NArg() != 0 {
		return o, errors.New("unexpected positional arguments")
	}
	return o, nil
}
func stagedSelection(only, to, from string) ([]int, error) {
	count := 0
	for _, s := range []string{only, to, from} {
		if s != "" {
			count++
		}
	}
	if count != 1 {
		return nil, errors.New("choose exactly one of --only, --to, --from")
	}
	ordinal := func(s string) int {
		for n, name := range stagedNames {
			if s == strconv.Itoa(n) || s == name {
				return n
			}
		}
		return 0
	}
	chosen := map[int]bool{}
	if only != "" {
		for _, s := range strings.Split(only, ",") {
			n := ordinal(strings.TrimSpace(s))
			if n == 0 || chosen[n] {
				return nil, errors.New("unknown or duplicate selected stage")
			}
			chosen[n] = true
		}
	} else {
		edge := ordinal(to + from)
		if edge == 0 {
			return nil, errors.New("unknown selected stage")
		}
		for _, n := range stagedOrdinals {
			if to != "" && n <= edge || from != "" && n >= edge {
				chosen[n] = true
			}
		}
	}
	var stages []int
	for _, n := range stagedOrdinals {
		if chosen[n] {
			stages = append(stages, n)
		}
	}
	return stages, nil
}
func stagedReadFile(path string) ([]byte, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	parent := filepath.Dir(absolute)
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || resolved != parent {
		return nil, errors.New("input file parent must be canonical")
	}
	f, err := openSnapshotRegularFile(parent, filepath.Base(absolute))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readBoundedSnapshotFile(f, 64<<20, "input")
}
func stagedQuery(ctx context.Context, h stagedHooks, req localRequest) ([]byte, error) {
	var out bytes.Buffer
	err := h.Query(ctx, req, &out)
	return bytes.TrimSpace(out.Bytes()), err
}
func stagedConfigIdentity(config []byte, stage int) string {
	var value map[string]json.RawMessage
	_ = json.Unmarshal(config, &value)
	var stages map[string]json.RawMessage
	_ = json.Unmarshal(value["stages"], &stages)
	projection := map[string]json.RawMessage{}
	for n, name := range map[int]string{70: "query_expander", 80: "candidate_matcher", 90: "result_selector", 100: "answer_synthesizer"} {
		if n <= stage {
			projection[name] = stages[name]
		}
	}
	for _, name := range []string{"profiles", "project_bindings", "query_service_implementation", "schema_version"} {
		projection[name] = value[name]
	}
	// Decode/re-encode to normalize whitespace and ordering inside RawMessage.
	var normalized any
	decoder := json.NewDecoder(bytes.NewReader(stagedJSON(projection)))
	decoder.UseNumber()
	_ = decoder.Decode(&normalized)
	return stagedHash(stagedJSON(normalized))
}
func stagedValidateSaved(saved map[int]stagedArtifact, files stagedTree, config, cases []byte) error {
	for stage, a := range saved {
		if a.Corpus != files.corpus() || a.Config != stagedConfigIdentity(config, stage) || a.Cases != stagedHash(cases) {
			return fmt.Errorf("saved stage %d corpus/config/cases mismatch; start from expansion", stage)
		}
		if stage > 70 {
			prev, ok := saved[stage-10]
			if !ok || a.Predecessor != stagedHash(stagedJSON(prev)) {
				return fmt.Errorf("saved stage %d dependency digest mismatch", stage)
			}
		}
	}
	return nil
}
func stagedValidateCorpus(files stagedTree) error {
	data, ok := files["cache/concepts.jsonl"]
	if !ok {
		return errors.New("missing concepts snapshot")
	}
	lines := bytes.Split(bytes.TrimSpace(data.Data), []byte("\n"))
	if len(lines) == 0 {
		return errors.New("empty concepts snapshot")
	}
	for _, line := range lines {
		var row struct {
			Slug string `json:"slug"`
		}
		if json.Unmarshal(line, &row) != nil || row.Slug == "" || row.Slug == "." || row.Slug == ".." || strings.ContainsAny(row.Slug, "/\\") {
			return errors.New("invalid concepts snapshot")
		}
		_, a := files["wiki/"+row.Slug+".md"]
		_, b := files["wiki/.drafts/"+row.Slug+".md"]
		if !a && !b {
			return errors.New("snapshot missing concept body")
		}
	}
	return nil
}
func stagedGeneration(source stagedSource, files stagedTree) (string, error) {
	for source.Kind == "fork" {
		if source.Origin == nil {
			return "", errors.New("invalid fork origin")
		}
		source = *source.Origin
	}
	if source.Kind == "gcs_published_generation" {
		var identity struct {
			Manifest struct {
				Generation string `json:"generation_id"`
			}
		}
		if json.Unmarshal(source.Published, &identity) != nil || identity.Manifest.Generation == "" {
			return "", errors.New("missing pinned generation")
		}
		return identity.Manifest.Generation, nil
	}
	return "local-" + files.corpus()[:24], nil
}
func stagedCopyArtifacts(saved map[int]stagedArtifact) map[int]stagedArtifact {
	out := map[int]stagedArtifact{}
	for k, v := range saved {
		out[k] = v
	}
	return out
}

func runStaged(ctx context.Context, o stagedOptions, h stagedHooks) (report *stagedReport, err error) {
	environment, err := stagedContainerBoundary(o)
	if err != nil {
		return nil, err
	}
	stages, err := stagedSelection(o.Only, o.To, o.From)
	if err != nil {
		return nil, err
	}
	selected := map[int]bool{}
	for _, s := range stages {
		selected[s] = true
	}
	first, last := stages[0], stages[len(stages)-1]
	if !stagedComponent.MatchString(o.User) || !stagedComponent.MatchString(o.Project) {
		return nil, errors.New("user/project must be safe identifiers")
	}
	if o.Timeout < 1 || o.Timeout > 3600 {
		return nil, errors.New("--timeout must be 1..3600 seconds")
	}
	if o.Output == "" || o.Config == "" {
		return nil, errors.New("--output and --query-config required")
	}
	count := 0
	for _, s := range []string{o.Raw, o.Snapshot, o.Fork} {
		if s != "" {
			count++
		}
	}
	if count != 1 {
		return nil, errors.New("choose --raw, --input-snapshot, or --fork")
	}
	if o.Grounded && !selected[100] {
		return nil, errors.New("--require-grounded requires stage 100")
	}
	if (o.Profile == "") != (o.Prompt == "") {
		return nil, errors.New("--query-profile and --query-prompt must be supplied together")
	}
	if o.Cases != "" && o.Suggested {
		return nil, errors.New("choose --cases or --suggested-cases")
	}
	if last >= 70 && o.Cases == "" && !o.Suggested {
		return nil, errors.New("query stages require --cases or --suggested-cases")
	}
	if h.Execution == "live_providers" && (selected[20] || selected[60] || selected[70] || selected[100]) && os.Getenv("DEEPSEEK_API_KEY") == "" {
		return nil, errors.New("selected inference stages require DEEPSEEK_API_KEY in the execution environment")
	}
	if err = o.Settings.validate(); err != nil {
		return nil, err
	}
	output, err := filepath.Abs(o.Output)
	if err != nil {
		return nil, err
	}
	if _, e := os.Lstat(output); !errors.Is(e, os.ErrNotExist) {
		return nil, errors.New("output already exists; choose a new run")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(output))
	if err != nil {
		return nil, errors.New("output parent must exist")
	}
	output = filepath.Join(parent, filepath.Base(output))
	o.Output = output
	for _, source := range []string{o.Raw, o.Fork, o.Snapshot} {
		if source != "" && !strings.HasPrefix(source, "gs://") {
			absolute, e := filepath.Abs(source)
			if e != nil {
				return nil, e
			}
			resolved, e := filepath.EvalSymlinks(absolute)
			if e != nil || resolved != absolute {
				return nil, errors.New("source must be canonical")
			}
			if output == resolved || strings.HasPrefix(output, resolved+string(os.PathSeparator)) {
				return nil, errors.New("output must be outside every donor/source directory")
			}
		}
	}
	config, err := stagedReadFile(o.Config)
	if err != nil {
		return nil, errors.New("query configuration unavailable")
	}
	sourceConfigDigest := stagedHash(config)
	request := localRequest{Operation: "validate-config", Config: o.Config}
	if _, err = stagedQuery(ctx, h, request); err != nil {
		return nil, err
	}
	if o.Profile != "" {
		request.Operation = "validate-profile"
		request.Profile = o.Profile
		request.Prompt = o.Prompt
		if _, err = stagedQuery(ctx, h, request); err != nil {
			return nil, err
		}
	}
	var cases []byte
	if o.Cases != "" {
		cases, err = stagedReadFile(o.Cases)
		if err != nil {
			return nil, errors.New("cases unavailable")
		}
		request.Operation = "validate"
		request.Cases = o.Cases
		if _, err = stagedQuery(ctx, h, request); err != nil {
			return nil, err
		}
	}
	impl := map[string]string{"runner_schema": stagedSchema}
	if h.Identity != "" {
		impl["query_binary"] = h.Identity
	} else {
		exe, e := os.Executable()
		if e != nil {
			return nil, e
		}
		identity, e := stagedExecutableDigest(exe)
		if e != nil {
			return nil, e
		}
		impl["query_binary"] = identity
	}
	needsSynto := selected[10] || selected[20] || selected[50] || experimentBuildID != ""
	if selected[50] || selected[60] {
		o.Worker, err = exec.LookPath(o.Worker)
		if err != nil {
			return nil, errors.New("selected stages require --worker-bin")
		}
		o.Worker, _ = filepath.Abs(o.Worker)
		identity, e := stagedExecutableDigest(o.Worker)
		if e != nil {
			return nil, e
		}
		impl["worker_binary"] = identity
	}
	if needsSynto {
		o.Synto, err = exec.LookPath(o.Synto)
		if err != nil {
			return nil, errors.New("selected stages require public synto executable")
		}
		o.Synto, _ = filepath.Abs(o.Synto)
		identity, e := stagedSyntoIdentity(o.Synto)
		if e != nil {
			return nil, e
		}
		impl["synto_implementation"] = identity
	}
	files := stagedTree{}
	saved := map[int]stagedArtifact{}
	source := stagedSource{Kind: "raw"}
	bindingSnapshot := o.Snapshot
	switch {
	case o.Raw != "":
		raw, e := stagedReadTree(o.Raw)
		if e != nil {
			return nil, e
		}
		if len(raw) == 0 {
			return nil, errors.New("raw fixture empty")
		}
		for p, f := range raw {
			if !strings.HasSuffix(p, ".md") {
				return nil, errors.New("raw fixture must contain only Markdown files")
			}
			files["raw/"+p] = f
		}
		if !selected[10] {
			return nil, errors.New("--raw requires stage 10")
		}
		source.RawDigest = raw.digest()
	case o.Fork != "":
		donor, e := stagedReadTree(o.Fork)
		if e != nil {
			return nil, e
		}
		var schema struct {
			Schema json.RawMessage `json:"schema"`
		}
		if json.Unmarshal(donor["run.json"].Data, &schema) != nil || string(schema.Schema) != strconv.Quote(stagedSchema) {
			return nil, errors.New("incompatible runner receipt: only lwc-local-go-v2 forks supported; retain Python evidence and explicitly import a corpus snapshot")
		}
		var manifest stagedReport
		if json.Unmarshal(donor["run.json"].Data, &manifest) != nil {
			return nil, errors.New("invalid experiment fork")
		}
		for k, v := range impl {
			if manifest.Implementation[k] != v {
				return nil, errors.New("fork implementation mismatch")
			}
		}
		checkpoint, latest := 0, 0
		for n := range manifest.Checkpoints {
			if n < first && n > checkpoint {
				checkpoint = n
			}
			if n > latest {
				latest = n
			}
		}
		if checkpoint == 0 {
			return nil, errors.New("fork has no checkpoint before first selected stage")
		}
		record := manifest.Checkpoints[checkpoint]
		files = donor.subset(fmt.Sprintf("snapshots/%d/", checkpoint))
		if files.digest() != record.Digest || !reflect.DeepEqual(files.mtimes(), record.Mtimes) {
			return nil, errors.New("fork snapshot content or mtime mismatch")
		}
		needed := 0
		for _, n := range stages {
			if n > 70 && !selected[n-10] {
				needed = max(needed, n-10)
			}
		}
		for n, a := range manifest.Checkpoints[latest].Artifacts {
			if n <= needed {
				saved[n] = a
			}
		}
		parts := strings.Split(manifest.Logical, "/")
		if len(parts) != 4 || parts[0] != "users" || parts[2] != "projects" || !stagedComponent.MatchString(parts[1]) || !stagedComponent.MatchString(parts[3]) {
			return nil, errors.New("invalid fork Project identity")
		}
		o.User, o.Project = parts[1], parts[3]
		o.Settings = manifest.Settings
		if err = o.Settings.validate(); err != nil {
			return nil, err
		}
		source = stagedSource{Kind: "fork", Digest: stagedHash(donor["run.json"].Data), Checkpoint: checkpoint, Origin: &manifest.Source}
		bindingSnapshot = filepath.Join(o.Fork, "snapshots", strconv.Itoa(checkpoint))
	case strings.HasPrefix(o.Snapshot, "gs://"):
		locator, e := parseGCSProjectRoot(o.Snapshot)
		if e != nil {
			return nil, e
		}
		if locator.bucket != "llm-wiki-data-dev" {
			return nil, errors.New("staged cloud import is restricted to llm-wiki-data-dev")
		}
		if first < 60 {
			return nil, errors.New("cloud snapshot supports downstream stages 60..100 only")
		}
		o.User, o.Project = locator.userID, locator.project
		source = stagedSource{Kind: "gcs_published_generation", LocatorHash: stagedHash([]byte(o.Snapshot))}
	default:
		files, err = stagedReadTree(o.Snapshot)
		if err != nil {
			return nil, err
		}
		source = stagedSource{Kind: "local_snapshot", Digest: files.digest()}
	}
	if selected[10] && o.Raw == "" {
		return nil, errors.New("stage 10 requires explicit --raw")
	}
	if selected[20] && !selected[10] {
		if len(files.subset("raw/")) == 0 || !bytes.Equal(files["synto.toml"].Data, o.Settings.toml()) {
			return nil, errors.New("stage 20 requires compatible stage 10 raw/config")
		}
		if stagedHasNativeState(files) {
			return nil, errors.New("fresh stage 20 rejects existing state DB")
		}
	}
	if selected[50] && !selected[20] {
		var receipt stagedNativeReceipt
		if len(files[".synto/state.db"].Data) == 0 || json.Unmarshal(files[".synto/local-run.json"].Data, &receipt) != nil || receipt.Schema != stagedSchema || receipt.Version != stagedSyntoVersion || receipt.Status != "success" || receipt.Validation != "public-exit-and-validated-agents-export" || receipt.Completeness != "not_proven" || receipt.Implementation != impl["synto_implementation"] || receipt.Digest != stagedNativeDigest(files) {
			return nil, errors.New("stage 50 requires successful Go native-run checkpoint")
		}
	}
	if last >= 60 && !selected[50] && source.Kind != "gcs_published_generation" {
		if err = stagedValidateCorpus(files); err != nil {
			return nil, err
		}
	}
	if o.Suggested && !selected[60] && source.Kind != "gcs_published_generation" {
		if _, ok := files["cache/suggested_queries.json"]; !ok {
			return nil, errors.New("suggested cases require stage 60 or saved artifact")
		}
	}
	for _, stage := range stages {
		if stage > 70 && !selected[stage-10] {
			if _, ok := saved[stage-10]; !ok {
				return nil, fmt.Errorf("stage %d requires compatible saved stage %d; use --fork", stage, stage-10)
			}
		}
	}
	bound := false
	bind := func(ctx context.Context, snapshot, configPath string, current stagedTree) ([]byte, error) {
		generation, e := stagedGeneration(source, current)
		if e != nil {
			return nil, e
		}
		return stagedQuery(ctx, h, localRequest{Operation: "bind-config", Config: configPath, Snapshot: snapshot, Project: o.Project, Generation: generation, Profile: o.Profile, Prompt: o.Prompt})
	}
	if o.Profile != "" && len(files) > 0 && !selected[50] && last >= 70 {
		config, err = bind(ctx, bindingSnapshot, o.Config, files)
		if err != nil {
			return nil, err
		}
		bound = true
	}
	if cases != nil {
		if err = stagedValidateSaved(saved, files, config, cases); err != nil {
			return nil, err
		}
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = os.Mkdir(output, 0700); err != nil {
		return nil, err
	}
	report = &stagedReport{Schema: stagedSchema, Status: "running", Implementation: impl, Source: source, Settings: o.Settings, ConfigDigest: stagedHash(config), SourceConfigDigest: sourceConfigDigest, Logical: "users/" + o.User + "/projects/" + o.Project, Selected: stages, Stages: []*stagedRecord{}, Checkpoints: map[int]stagedCheckpoint{}, Quality: stagedQuality{"none", "not_requested"}, Execution: h.Execution}
	report.Environment = environment
	if o.Grounded {
		report.Quality = stagedQuality{"grounded-answer", "pending"}
	}
	// Every post-creation failure has an atomic receipt; successful checkpoints remain forkable.
	defer func() {
		if err != nil {
			report.Status = "failure"
			if len(report.Checkpoints) > 0 {
				report.Status = "partial"
			}
			report.Failure = err.Error()
			if len(report.Stages) > 0 {
				report.Stages[len(report.Stages)-1].Status = "failure"
			}
		}
		writeErr := stagedAtomic(filepath.Join(output, "run.json"), report)
		if err == nil {
			err = writeErr
		}
	}()
	if err = stagedAtomic(filepath.Join(output, "run.json"), report); err != nil {
		return report, err
	}
	for _, dir := range []string{"home", "xdg", "artifacts", "snapshots"} {
		if err = os.Mkdir(filepath.Join(output, dir), 0700); err != nil {
			return report, err
		}
	}
	if err = stagedContainerTemp(output); err != nil {
		return report, err
	}
	child := func(ctx context.Context, argv []string, cwd string, inference bool) ([]byte, error) {
		childCtx, cancel := context.WithTimeout(ctx, time.Duration(o.Timeout)*time.Second)
		defer cancel()
		return h.Child(childCtx, argv, stagedEnvironment(output, inference, false), cwd)
	}
	project := filepath.Join(output, "work", report.Logical)
	if source.Kind == "gcs_published_generation" {
		importCtx, cancel := context.WithTimeout(ctx, time.Duration(o.Timeout)*time.Second)
		imported := filepath.Join(output, "imported")
		source.Published, err = stagedQuery(importCtx, h, localRequest{Operation: "import", Snapshot: o.Snapshot, Destination: imported})
		cancel()
		if err != nil {
			partials, _ := filepath.Glob(filepath.Join(output, ".import-*"))
			for _, p := range append(partials, imported) {
				_ = os.RemoveAll(p)
			}
			return report, err
		}
		files, err = stagedReadTree(imported)
		if err != nil {
			return report, err
		}
		source.Digest = files.digest()
		source.MtimePolicy = "manifest-created-at"
	}
	source.MtimeDigest = stagedHash(stagedJSON(files.mtimes()))
	report.Source = source
	if err = stagedPutTree(filepath.Join(output, "input"), files); err != nil {
		return report, err
	}
	work := files
	if selected[10] {
		work = stagedTree{}
	}
	if err = stagedPutTree(project, work); err != nil {
		return report, err
	}
	if err = os.WriteFile(filepath.Join(project, ".lwc-experiment-workspace"), []byte("exclusive local copy\n"), 0600); err != nil {
		return report, err
	}
	configPath := filepath.Join(output, "query-config.json")
	casesPath := filepath.Join(output, "cases.jsonl")
	if err = os.WriteFile(configPath, config, 0600); err != nil {
		return report, err
	}
	if cases != nil {
		if err = os.WriteFile(casesPath, cases, 0600); err != nil {
			return report, err
		}
	}
	if needsSynto {
		version, e := child(ctx, []string{o.Synto, "--version"}, output, false)
		if e != nil {
			return report, e
		}
		actual, e := stagedPublicVersion(version)
		if e != nil {
			return report, e
		}
		report.Implementation["synto_version"] = actual
		if actual != stagedSyntoVersion {
			return report, errors.New("unsupported public Synto version")
		}
	}
	if selected[10] {
		if err = os.WriteFile(filepath.Join(project, "synto.toml"), o.Settings.toml(), 0600); err != nil {
			return report, err
		}
		if _, err = child(ctx, []string{o.Synto, "init", project, "--non-interactive"}, project, false); err != nil {
			return report, err
		}
		initialized, e := stagedReadTree(project)
		if e != nil {
			return report, e
		}
		if !bytes.Equal(initialized["synto.toml"].Data, o.Settings.toml()) || stagedHasNativeState(initialized) {
			return report, errors.New("public initialization changed configuration or created state DB")
		}
	}
	if last >= 60 && !selected[50] {
		if _, err = stagedQuery(ctx, h, localRequest{Operation: "validate-snapshot", Config: configPath, Snapshot: project}); err != nil {
			return report, err
		}
	}
	for _, stage := range stages {
		started := time.Now()
		record := &stagedRecord{Ordinal: stage, Name: stagedNames[stage], Status: "running", Execution: "fresh"}
		report.Stages = append(report.Stages, record)
		if err = stagedAtomic(filepath.Join(output, "run.json"), report); err != nil {
			return report, err
		}
		stageCtx, cancel := context.WithTimeout(ctx, time.Duration(o.Timeout)*time.Second)
		err = func() error {
			if e := stageCtx.Err(); e != nil {
				return errors.New("stage canceled")
			}
			switch stage {
			case 10:
				// init creates raw/, so install only into that empty, private directory.
				if e := os.Remove(filepath.Join(project, "raw")); e != nil {
					return e
				}
				if e := stagedPutTree(filepath.Join(project, "raw"), files.subset("raw/")); e != nil {
					return e
				}
			case 20:
				before, e := stagedReadTree(project)
				if e != nil {
					return e
				}
				if stagedHasNativeState(before) {
					return errors.New("fresh stage 20 rejects existing state DB")
				}
				if !bytes.Equal(before["synto.toml"].Data, o.Settings.toml()) {
					return errors.New("unsafe native configuration")
				}
				if _, e = child(stageCtx, []string{o.Synto, "run", "--vault", project, "--auto-approve", "--max-rounds", "2", "--min-confidence", "0"}, project, true); e != nil {
					return e
				}
				export, e := os.MkdirTemp(output, "producer-export-")
				if e != nil {
					return e
				}
				defer os.RemoveAll(export)
				if _, e = child(stageCtx, []string{o.Synto, "pack", "export", "--vault", project, "--target", "agents", "--out", export}, project, false); e != nil {
					return e
				}
				if e = stagedValidateProducer(project, export, before); e != nil {
					return e
				}
				native, e := stagedReadTree(project)
				if e != nil {
					return e
				}
				return stagedAtomic(filepath.Join(project, ".synto/local-run.json"), stagedNativeReceipt{Version: report.Implementation["synto_version"], Schema: stagedSchema, Status: "success", Validation: "public-exit-and-validated-agents-export", Completeness: "not_proven", Implementation: impl["synto_implementation"], Digest: stagedNativeDigest(native)})
			case 50, 60:
				command := "suggested"
				if stage == 50 {
					command = "index"
				}
				argv := []string{o.Worker, "local-experiment", "--local-vault", project}
				if stage == 50 {
					argv = append(argv, "--synto-bin", o.Synto)
				}
				argv = append(argv, command)
				if _, e := child(stageCtx, argv, project, stage == 60); e != nil {
					return e
				}
				if stage == 50 {
					_, e := stagedQuery(stageCtx, h, localRequest{Operation: "validate-snapshot", Config: configPath, Snapshot: project})
					return e
				}
			default:
				if cases == nil {
					data, e := stagedQuery(stageCtx, h, localRequest{Operation: "suggested-cases", Config: configPath, Snapshot: project})
					if e != nil {
						return e
					}
					var generated []json.RawMessage
					if json.Unmarshal(data, &generated) != nil || len(generated) == 0 {
						return errors.New("invalid suggested cases")
					}
					for _, c := range generated {
						cases = append(cases, append(c, '\n')...)
					}
					if e = os.WriteFile(casesPath, cases, 0600); e != nil {
						return e
					}
				}
				current, e := stagedReadTree(project)
				if e != nil {
					return e
				}
				if o.Profile != "" && !bound {
					config, e = bind(stageCtx, project, configPath, current)
					if e != nil {
						return e
					}
					if e = os.WriteFile(configPath, config, 0600); e != nil {
						return e
					}
					report.ConfigDigest = stagedHash(config)
					bound = true
				}
				if e = stagedValidateSaved(saved, current, config, cases); e != nil {
					return e
				}
				previous, ok := saved[stage-10]
				if stage > 70 && !ok {
					return fmt.Errorf("stage %d requires saved stage %d", stage, stage-10)
				}
				generation, e := stagedGeneration(source, current)
				if e != nil {
					return e
				}
				data, e := stagedQuery(stageCtx, h, localRequest{Operation: "query", Snapshot: project, Config: configPath, Cases: casesPath, Project: o.Project, Generation: generation, Stage: stage, Saved: previous.Data})
				if e != nil {
					return e
				}
				var response map[string]localCaseResult
				if json.Unmarshal(data, &response) != nil || len(response) == 0 {
					return errors.New("invalid or empty query stage response")
				}
				artifact := stagedArtifact{Corpus: current.corpus(), Config: stagedConfigIdentity(config, stage), Cases: stagedHash(cases), Data: response}
				if ok {
					artifact.Predecessor = stagedHash(stagedJSON(previous))
				}
				saved[stage] = artifact
				if e = stagedAtomic(filepath.Join(output, "artifacts", strconv.Itoa(stage)+".json"), artifact); e != nil {
					return e
				}
				record.Cases = map[string]string{}
				allGood := true
				mechanism := true
				expected, e := readCases(casesPath)
				if e != nil {
					return errors.New("invalid frozen cases")
				}
				if len(expected) != len(response) {
					return errors.New("query stage omitted cases")
				}
				for _, c := range expected {
					if _, ok := response[c.ID]; !ok {
						return errors.New("query stage omitted case")
					}
				}
				for id, c := range response {
					record.Cases[id] = c.Status
					if c.Status != "success" {
						allGood = false
					}
					if c.Status != "success" && c.Status != "no_grounded_answer" {
						mechanism = false
					}
				}
				if stage == 100 && o.Grounded {
					report.Quality.Status = "failed"
					if allGood {
						report.Quality.Status = "passed"
					}
				}
				if !mechanism {
					return fmt.Errorf("stage %d produced failed/partial cases; inspect local artifacts", stage)
				}
				later := []int{}
				for n := range saved {
					if n > stage {
						later = append(later, n)
					}
				}
				sort.Ints(later)
				for _, n := range later {
					pred, ok := saved[n-10]
					if !ok || saved[n].Predecessor != stagedHash(stagedJSON(pred)) {
						delete(saved, n)
					}
				}
			}
			return nil
		}()
		cancel()
		if err != nil {
			return report, err
		}
		snapshot, e := stagedReadTree(project)
		if e != nil {
			return report, e
		}
		if raw := files.subset("raw/"); len(raw) > 0 {
			current := snapshot.subset("raw/")
			if raw.digest() != current.digest() || !reflect.DeepEqual(raw.mtimes(), current.mtimes()) {
				return report, errors.New("stage changed raw source bytes or mtimes")
			}
		}
		if e = stagedPutTree(filepath.Join(output, "snapshots", strconv.Itoa(stage)), snapshot); e != nil {
			return report, e
		}
		record.Status = "success"
		record.Duration = time.Since(started).Seconds()
		report.Checkpoints[stage] = stagedCheckpoint{snapshot.digest(), snapshot.mtimes(), stagedCopyArtifacts(saved)}
		if err = stagedAtomic(filepath.Join(output, "run.json"), report); err != nil {
			return report, err
		}
	}
	report.Status = "success"
	return report, nil
}
