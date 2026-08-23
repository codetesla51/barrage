package barrage

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"sync"
	"time"
)

//go:embed all:webui
var webuiFS embed.FS

// uiRun is one load-test run started from the web UI.
type uiRun struct {
	ID        string
	CreatedAt time.Time
	Duration  time.Duration

	mu       sync.Mutex
	state    string // running | done | error
	errMsg   string
	started  time.Time
	finished time.Time

	config *OrchestratorConfig
}

func (r *uiRun) snapshot() (string, float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var elapsed float64
	switch r.state {
	case "running":
		elapsed = time.Since(r.started).Seconds()
	default:
		elapsed = r.finished.Sub(r.started).Seconds()
	}
	return r.state, elapsed
}

// UIServer hosts the web UI and its JSON API on localhost.
type UIServer struct {
	addr    string
	runsDir string

	mu   sync.Mutex
	runs map[string]*uiRun
}

// NewUIServer creates a server bound to addr. Run artifacts (JSON exports and
// HTML reports) are stored under ~/.barrage/ui/runs so past runs survive
// restarts and feed the Recent Runs list.
func NewUIServer(addr string) *UIServer {
	home, _ := os.UserHomeDir()
	return &UIServer{
		addr:    addr,
		runsDir: filepath.Join(home, ".barrage", "ui", "runs"),
		runs:    make(map[string]*uiRun),
	}
}

// ListenAndServe blocks serving the UI. Static assets are embedded in the
// binary; no separate build step or asset directory is needed.
func (s *UIServer) ListenAndServe() error {
	sub, err := fs.Sub(webuiFS, "webui")
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("POST /api/validate", s.handleValidate)
	mux.HandleFunc("POST /api/runs", s.handleStartRun)
	mux.HandleFunc("GET /api/runs", s.handleListRuns)
	mux.HandleFunc("GET /api/runs/{id}/status", s.handleRunStatus)
	mux.HandleFunc("GET /api/runs/{id}/report", s.handleRunReport)
	mux.HandleFunc("GET /api/runs/{id}/json", s.handleRunJSON)
	mux.HandleFunc("POST /api/compare", s.handleCompare)

	srv := &http.Server{
		Addr:              s.addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return srv.ListenAndServe()
}

func (s *UIServer) writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// handleValidate parses a posted YAML config with the exact loader the CLI
// uses, so the server rejects unknown keys and invalid fields identically.
func (s *UIServer) handleValidate(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		s.writeErr(w, http.StatusBadRequest, "reading body")
		return
	}
	cfg, err := LoadConfigBytes(body)
	if err != nil {
		s.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "runners": configuredRunnerNames(cfg)})
}

func configuredRunnerNames(cfg *OrchestratorConfig) []string {
	names := make([]string, 0, 4)
	if cfg.HTTP != nil {
		names = append(names, "http")
	}
	if cfg.DB != nil {
		names = append(names, "db")
	}
	if cfg.Redis != nil {
		names = append(names, "redis")
	}
	for _, sc := range cfg.Scenarios {
		name := sc.Name
		if name == "" {
			name = "scenario"
		}
		names = append(names, name)
	}
	return names
}

type startRunRequest struct {
	YAML              string `json:"yaml"`
	HTTPThresholdMS   int64  `json:"http_threshold_ms"`
	DBThresholdMS     int64  `json:"db_threshold_ms"`
	RedisThresholdMS  int64  `json:"redis_threshold_ms"`
}

// handleStartRun validates the config, then starts the run in the background.
// Only one UI run may execute at a time; a second attempt is rejected with 409.
func (s *UIServer) handleStartRun(w http.ResponseWriter, r *http.Request) {
	var req startRunRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		s.writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	cfg, err := LoadConfigBytes([]byte(req.YAML))
	if err != nil {
		s.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	s.mu.Lock()
	for id, run := range s.runs {
		state, _ := run.snapshot()
		if state == "running" {
			s.mu.Unlock()
			s.writeErr(w, http.StatusConflict, "a run is already in progress ("+id+")")
			return
		}
	}
	id := fmt.Sprintf("%d", time.Now().UnixMilli())
	run := &uiRun{
		ID:        id,
		CreatedAt: time.Now(),
		Duration:  time.Duration(cfg.Duration),
		state:     "running",
		started:   time.Now(),
		config:    cfg,
	}
	s.runs[id] = run
	s.mu.Unlock()

	go s.executeRun(run, req)
	writeJSON(w, map[string]any{"id": id, "duration_s": run.Duration.Seconds()})
}

// executeRun runs the orchestrator synchronously, then persists the JSON
// export and HTML report under the runs directory.
func (s *UIServer) executeRun(run *uiRun, req startRunRequest) {
	result, err := Orchestrator(*run.config)
	if err != nil {
		run.mu.Lock()
		run.state, run.errMsg, run.finished = "error", err.Error(), time.Now()
		run.mu.Unlock()
		return
	}

	spikes := Correlate(result,
		time.Duration(req.HTTPThresholdMS)*time.Millisecond,
		time.Duration(req.DBThresholdMS)*time.Millisecond,
		time.Duration(req.RedisThresholdMS)*time.Millisecond,
	)

	dir := filepath.Join(s.runsDir, run.ID)
	if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
		run.mu.Lock()
		run.state, run.errMsg, run.finished = "error", mkErr.Error(), time.Now()
		run.mu.Unlock()
		return
	}

	data := NewReportData(result, spikes)
	data.Duration = time.Duration(run.config.Duration).String()
	data.Ramp = time.Duration(run.config.Ramp).String()
	data.Concurrency = run.config.Concurrency

	jsonPath := filepath.Join(dir, "results.json")
	if jErr := ExportJSON(data, jsonPath); jErr != nil {
		run.mu.Lock()
		run.state, run.errMsg, run.finished = "error", jErr.Error(), time.Now()
		run.mu.Unlock()
		return
	}

	reportPath := filepath.Join(dir, "report.html")
	f, fErr := os.Create(reportPath)
	if fErr != nil {
		run.mu.Lock()
		run.state, run.errMsg, run.finished = "error", fErr.Error(), time.Now()
		run.mu.Unlock()
		return
	}
	defer f.Close()
	if rErr := RenderHTML(data, "templates/report.html", f); rErr != nil {
		run.mu.Lock()
		run.state, run.errMsg, run.finished = "error", rErr.Error(), time.Now()
		run.mu.Unlock()
		return
	}

	// keep the config alongside the results for future diffing
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(req.YAML), 0o644)

	run.mu.Lock()
	run.state, run.finished = "done", time.Now()
	run.mu.Unlock()
}

// uiRunSummary is one row of the Recent Runs list.
type uiRunSummary struct {
	ID        string             `json:"id"`
	CreatedAt time.Time          `json:"created_at"`
	Duration  string             `json:"duration"`
	Runners   []map[string]any   `json:"runners"`
	State     string             `json:"state"`
	Error     string             `json:"error,omitempty"`
}

func (s *UIServer) handleListRuns(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	mem := make(map[string]*uiRun, len(s.runs))
	for id, run := range s.runs {
		mem[id] = run
	}
	s.mu.Unlock()

	dirs, _ := os.ReadDir(s.runsDir)
	ids := make([]string, 0, len(dirs))
	for _, e := range dirs {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	// include in-memory runs that have not reached disk yet (running/error)
	for id, run := range mem {
		state, _ := run.snapshot()
		if state == "running" || state == "error" {
			found := false
			for _, known := range ids {
				if known == id {
					found = true
					break
				}
			}
			if !found {
				ids = append(ids, id)
			}
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))

	out := make([]uiRunSummary, 0, len(ids))
	for _, id := range ids {
		sum, err := s.readRunSummary(id)
		if err != nil {
			// not on disk (or unreadable): fall back to in-memory state
			run, ok := mem[id]
			if !ok {
				continue
			}
			state, elapsed := run.snapshot()
			sum = uiRunSummary{
				ID:        id,
				CreatedAt: run.CreatedAt,
				Duration:  fmt.Sprintf("%.0fs", elapsed),
				State:     state,
				Error:     run.errMsgLocked(),
			}
		}
		if run, ok := mem[id]; ok {
			state, _ := run.snapshot()
			if state == "error" {
				sum.State = "error"
				sum.Error = run.errMsgLocked()
			} else if state == "running" {
				sum.State = "running"
			}
		}
		out = append(out, sum)
	}
	writeJSON(w, out)
}

func (r *uiRun) errMsgLocked() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.errMsg
}

func (s *UIServer) readRunSummary(id string) (uiRunSummary, error) {
	buf, err := os.ReadFile(filepath.Join(s.runsDir, id, "results.json"))
	if err != nil {
		return uiRunSummary{}, err
	}
	var jr JSONReport
	if err := json.Unmarshal(buf, &jr); err != nil {
		return uiRunSummary{}, err
	}
	created := time.Time{}
	if fi, err := os.Stat(filepath.Join(s.runsDir, id)); err == nil {
		created = fi.ModTime()
	}
	runners := make([]map[string]any, 0, len(jr.Runners))
	for _, rn := range jr.Runners {
		runners = append(runners, map[string]any{"name": rn.Name, "p99_ms": rn.P99MS, "success": rn.Success})
	}
	return uiRunSummary{
		ID:        id,
		CreatedAt: created,
		Duration:  jr.Duration,
		Runners:   runners,
		State:     "done",
	}, nil
}

func (s *UIServer) handleRunStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	run := s.runs[r.PathValue("id")]
	s.mu.Unlock()
	if run == nil {
		s.writeErr(w, http.StatusNotFound, "unknown run")
		return
	}
	state, elapsed := run.snapshot()
	resp := map[string]any{"state": state, "elapsed_s": elapsed, "duration_s": run.Duration.Seconds()}
	if run.errMsgLocked() != "" {
		resp["error"] = run.errMsgLocked()
	}
	writeJSON(w, resp)
}

func (s *UIServer) serveRunFile(w http.ResponseWriter, r *http.Request, name, ctype string) {
	id := r.PathValue("id")
	path := filepath.Join(s.runsDir, filepath.Base(id), name)
	buf, err := os.ReadFile(path)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, "report not ready")
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Write(buf)
}

func (s *UIServer) handleRunReport(w http.ResponseWriter, r *http.Request) {
	s.serveRunFile(w, r, "report.html", "text/html; charset=utf-8")
}

func (s *UIServer) handleRunJSON(w http.ResponseWriter, r *http.Request) {
	s.serveRunFile(w, r, "results.json", "application/json")
}

type compareRequest struct {
	BaselineID string `json:"baseline_id"`
	CurrentID  string `json:"current_id"`
	FailOnMS   int64  `json:"fail_on_ms"`
}

// handleCompare diffs two stored runs using the same engine as the CLI's
// compare command, and returns plain rows for the UI to render.
func (s *UIServer) handleCompare(w http.ResponseWriter, r *http.Request) {
	var req compareRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		s.writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	load := func(id string) (*JSONReport, error) {
		dir := filepath.Join(s.runsDir, filepath.Base(id), "results.json")
		buf, err := os.ReadFile(dir)
		if err != nil {
			return nil, fmt.Errorf("run %q has no results.json", id)
		}
		var jr JSONReport
		if err := json.Unmarshal(buf, &jr); err != nil {
			return nil, err
		}
		return &jr, nil
	}
	baseline, err := load(req.BaselineID)
	if err != nil {
		s.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	current, err := load(req.CurrentID)
	if err != nil {
		s.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	failOn := req.FailOnMS
	if failOn <= 0 {
		failOn = 100
	}
	rows := CompareRun(baseline, current)
	regressions := 0
	outRows := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		reg := row.Regressed(failOn)
		if reg {
			regressions++
		}
		outRows = append(outRows, map[string]any{
			"name": row.Name, "baseline_p99_ms": row.BaselineP99,
			"current_p99_ms": row.CurrentP99, "pct_change": row.PctChange,
			"regressed": reg,
		})
	}
	spikes := CompareSpikes(baseline, current)
	outSpikes := make([]map[string]any, 0, len(spikes))
	for _, sp := range spikes {
		outSpikes = append(outSpikes, map[string]any{
			"bucket_time": sp.BucketTime, "runner": sp.Runner, "status": sp.Status,
		})
	}
	timeline := BuildCompareTimeline(baseline, current)
	writeJSON(w, map[string]any{
		"fail_on_ms":  failOn,
		"regressions": regressions,
		"rows":        outRows,
		"spikes":      outSpikes,
		"timeline":    timeline,
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
