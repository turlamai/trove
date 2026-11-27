// Package api serves the daemon's JSON API over a local Unix socket. The
// PHP gateway in the WebGUI is the only intended client; the socket refuses
// any peer whose credentials do not match the daemon owner (root), and every
// request must carry the per-start token from a 0600 file.
package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"sort"
	"crypto/subtle"
	"strconv"
	"strings"
	"time"

	"github.com/turlamai/trove/daemon/internal/config"
	"github.com/turlamai/trove/daemon/internal/executor"
	"github.com/turlamai/trove/daemon/internal/jobs"
	"github.com/turlamai/trove/daemon/internal/model"
	"github.com/turlamai/trove/daemon/internal/planner"
	"github.com/turlamai/trove/daemon/internal/resolver"
	"github.com/turlamai/trove/daemon/internal/roots"
	"github.com/turlamai/trove/daemon/internal/troveerr"
	"github.com/turlamai/trove/daemon/internal/unraid"
)

// Version is injected at build time.
var Version = "dev"

// StartedAt records daemon start (uptime reporting in debug diagnostics).
var StartedAt = time.Now()

// Deps bundles the server's collaborators.
type Deps struct {
	Resolver *resolver.Resolver
	Registry *roots.Registry
	Planner  *planner.Planner
	Manager  *jobs.Manager      // nil in read-only mode
	Executor *executor.Executor // nil in read-only mode
	Cfg      func() *config.Config
	Uraid    func() unraid.State
	Token    string
}

// New builds the HTTP handler wrapped in the gateway-token middleware.
func New(d Deps) http.Handler {
	return tokenMiddleware(d, routes(d))
}

func routes(d Deps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": Version})
	})
	mux.HandleFunc("GET /api/v1/capabilities", d.handleCapabilities)
	mux.HandleFunc("GET /api/v1/roots", d.handleRoots)
	mux.HandleFunc("GET /api/v1/entries", d.handleEntries)
	mux.HandleFunc("GET /api/v1/entry", d.handleEntry)
	mux.HandleFunc("GET /api/v1/download", d.handleDownload)
	mux.HandleFunc("GET /api/v1/search", d.handleSearch)
	mux.HandleFunc("POST /api/v1/operations/plan", d.handlePlan)
	mux.HandleFunc("POST /api/v1/operations/execute", d.handleExecute)
	mux.HandleFunc("POST /api/v1/jobs", d.handleJobSubmit)
	mux.HandleFunc("GET /api/v1/jobs", d.handleJobList)
	mux.HandleFunc("GET /api/v1/jobs/{id}", d.handleJobGet)
	mux.HandleFunc("POST /api/v1/jobs/{id}/cancel", d.handleJobCancel)
	mux.HandleFunc("POST /api/v1/jobs/{id}/retry", d.handleJobRetry)
	mux.HandleFunc("POST /api/v1/jobs/{id}/resolve", d.handleJobResolve)
	mux.HandleFunc("GET /api/v1/jobs/{id}/events", d.handleJobEvents)
	mux.HandleFunc("GET /api/v1/debug/log", d.handleDebugLog)
	mux.HandleFunc("GET /api/v1/settings", d.handleSettingsGet)
	mux.HandleFunc("PUT /api/v1/settings", d.handleSettingsPut)
	return mux
}

// tokenMiddleware enforces the per-start token on every request. The token
// lives in a 0600 root-only file; anything that can read it may call.
func tokenMiddleware(d Deps, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("X-Trove-Token")
		if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(d.Token)) != 1 {
			writeErr(w, troveerr.New(troveerr.Unauthorized, "missing or invalid gateway token"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// writeJSON writes a JSON response.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr writes the contract error envelope.
func writeErr(w http.ResponseWriter, err error) {
	code := troveerr.Of(err)
	writeJSON(w, troveerr.HTTPStatus(code), map[string]any{
		"error": map[string]any{
			"code":        string(code),
			"message":     err.Error(),
			"recoverable": troveerr.Recoverable(code),
			"requestId":   fmt.Sprintf("req_%d", time.Now().UnixNano()),
		},
	})
}

func decodeBody(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		return troveerr.New(troveerr.BadRequest, "invalid JSON body")
	}
	return nil
}

// ---------------------------------------------------------------------------
// handlers
// ---------------------------------------------------------------------------

func (d Deps) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	cfg := d.Cfg()
	dataOK := d.Manager != nil
	writeJSON(w, http.StatusOK, model.Capabilities{
		Version:       Version,
		APIVersion:    "v1",
		ReadOK:        true,
		WriteOK:       dataOK,
		ReadOnlyWhy:   readOnlyWhy(cfg),
		DataDir:       cfg.DataDir,
		DataDirOK:     dataOK,
		ExpertMode:    cfg.ExpertMode,
		PhysicalWrite: cfg.PhysicalWrite,
		Concurrency:   cfg.Concurrency,
		Openat2:       d.Resolver.OpenAT2Supported(),
		ArrayState:    d.Uraid().MDState,
		Platform:      "linux",
		Actions:       []string{"copy", "move", "delete", "rename", "mkdir", "hash"},
	})
}

func readOnlyWhy(cfg *config.Config) string {
	if cfg.DataDir == "" {
		return "no data directory configured (Settings → Trove); write jobs are disabled"
	}
	return ""
}

func (d Deps) handleRoots(w http.ResponseWriter, r *http.Request) {
	cfg := d.Cfg()
	type rootOut struct {
		Kind      string `json:"kind"`
		ID        string `json:"id"`
		Name      string `json:"name"`
		Path      string `json:"path"`
		FSType    string `json:"fsType,omitempty"`
		ReadOnly  bool   `json:"readOnly"`
		Protected bool   `json:"protected"`
		Expert    bool   `json:"expertOnly"`
	}
	out := []rootOut{}
	for _, rt := range d.Registry.List() {
		out = append(out, rootOut{
			Kind:      string(rt.Ref),
			ID:        rt.ID,
			Name:      rt.ID,
			Path:      rt.Path,
			FSType:    rt.FSType,
			ReadOnly:  rt.ReadOnly,
			Protected: d.Registry.IsProtected(rt),
			Expert:    rt.Ref != model.KindShare || cfg.ExpertMode,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"roots": out})
}

func parseRootQ(r *http.Request) (model.RootRef, error) {
	raw := r.URL.Query().Get("rootId")
	kind, id, ok := strings.Cut(raw, ":")
	if !ok {
		return model.RootRef{}, troveerr.New(troveerr.BadRequest, "rootId must be kind:id")
	}
	return model.RootRef{Kind: model.RootKind(kind), ID: id}, nil
}

func (d Deps) handleEntries(w http.ResponseWriter, r *http.Request) {
	ref, err := parseRootQ(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	dir := r.URL.Query().Get("path")
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	sortKey := r.URL.Query().Get("sort")
	if sortKey == "" {
		sortKey = "name"
	}
	desc := r.URL.Query().Get("dir") == "desc"
	filterStr := strings.ToLower(r.URL.Query().Get("filter"))

	res, err := d.Resolver.Resolve(ref, dir, false)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer res.Close()
	if !res.Exists {
		writeErr(w, troveerr.New(troveerr.NotFound, "directory not found"))
		return
	}
	if !res.IsDir() {
		writeErr(w, troveerr.New(troveerr.BadRequest, "not a directory"))
		return
	}
	dirFd, err := resolver.OpenResolvedDir(res)
	if err != nil {
		writeErr(w, troveerr.Wrap(troveerr.IOFailure, err, "open directory"))
		return
	}
	defer resolver.CloseFD(dirFd)

	entries, err := resolver.ListDir(dirFd)
	if err != nil {
		writeErr(w, troveerr.Wrap(troveerr.IOFailure, err, "scan directory"))
		return
	}

	// filter
	if filterStr != "" {
		filtered := entries[:0]
		for _, e := range entries {
			if strings.Contains(strings.ToLower(e.Name), filterStr) {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}

	// sort (dirs first)
	nameOf := func(i int) string { return entries[i].Name }
	less := func(i, j int) bool { return false }
	switch sortKey {
	case "size":
		less = func(i, j int) bool { return entries[i].Stat.Size < entries[j].Stat.Size }
	case "mtime":
		less = func(i, j int) bool {
			if entries[i].Stat.Mtime != entries[j].Stat.Mtime {
				return entries[i].Stat.Mtime < entries[j].Stat.Mtime
			}
			return entries[i].Stat.MtimeN < entries[j].Stat.MtimeN
		}
	case "type":
		less = func(i, j int) bool { return entries[i].Stat.Kind < entries[j].Stat.Kind }
	default:
		less = func(i, j int) bool { return nameOf(i) < nameOf(j) }
	}
	dirFirst := r.URL.Query().Get("dirsFirst") != "false"
	sort.SliceStable(entries, func(i, j int) bool {
		if dirFirst {
			id := entries[i].Stat.Kind == model.EntryDir
			jd := entries[j].Stat.Kind == model.EntryDir
			if id != jd {
				return id
			}
		}
		if desc {
			return less(j, i)
		}
		return less(i, j)
	})

	total := len(entries)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	type entryOut struct {
		model.Entry
		Dev uint64 `json:"dev"`
		Ino uint64 `json:"ino"`
	}
	page := make([]entryOut, 0, end-offset)
	for _, e := range entries[offset:end] {
		page = append(page, entryOut{
			Entry: model.Entry{
				Name:   e.Name,
				Kind:   e.Stat.Kind,
				Size:   e.Stat.Size,
				Mode:   modeString(e.Stat.Mode),
				Mtime:  time.Unix(e.Stat.Mtime, int64(e.Stat.MtimeN)).UTC(),
				Target: e.Stat.SymLink,
			},
			Dev: e.Stat.Dev,
			Ino: e.Stat.Ino,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"root": ref, "path": dir, "total": total, "offset": offset, "entries": page,
	})
}

func modeString(mode uint32) string {
	bits := []struct {
		m uint32
		c byte
	}{{4, 'r'}, {2, 'w'}, {1, 'x'}}
	out := make([]byte, 9)
	shift := uint32(6)
	for tri := 0; tri < 3; tri++ {
		for bi, b := range bits {
			if mode>>shift&b.m != 0 {
				out[tri*3+bi] = b.c
			} else {
				out[tri*3+bi] = '-'
			}
		}
		shift -= 3
	}
	return string(out)
}

func (d Deps) handleEntry(w http.ResponseWriter, r *http.Request) {
	ref, err := parseRootQ(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	p := r.URL.Query().Get("path")
	res, err := d.Resolver.ResolveExists(ref, p, false)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer res.Close()
	root, _ := d.Resolver.RootOf(ref)
	protected := root != nil && d.Registry.IsProtected(root)
	writeJSON(w, http.StatusOK, map[string]any{
		"root":      ref,
		"path":      p,
		"name":      path.Base("/" + p),
		"kind":      res.Stat.Kind,
		"size":      res.Stat.Size,
		"mode":      modeString(res.Stat.Mode),
		"mtime":     time.Unix(res.Stat.Mtime, int64(res.Stat.MtimeN)).UTC(),
		"uid":       res.Stat.Uid,
		"gid":       res.Stat.Gid,
		"nlink":     res.Stat.Nlink,
		"target":    res.Stat.SymLink,
		"fullPath":  res.FullPath(),
		"protected": protected,
		"sha256":    nil, // computed on demand via a hash job
	})
}

// handleDownload streams one file (read-only, single file, attachment).
func (d Deps) handleDownload(w http.ResponseWriter, r *http.Request) {
	ref, err := parseRootQ(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	p := r.URL.Query().Get("path")
	res, err := d.Resolver.ResolveExists(ref, p, false)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer res.Close()
	if res.Stat.Kind != model.EntryFile {
		writeErr(w, troveerr.New(troveerr.UnsupportedType, "only regular files can be downloaded"))
		return
	}
	fd, err := resolver.OpenFileNoFollow(res.ParentFd, res.Name, os.O_RDONLY, 0)
	if err != nil {
		writeErr(w, troveerr.Wrap(troveerr.IOFailure, err, "open file"))
		return
	}
	defer resolver.CloseFD(fd)

	name := path.Base("/" + p)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition",
		"attachment; filename*=UTF-8''"+strings.ReplaceAll(urlEscape(name), "%", "%25"))
	w.Header().Set("Content-Length", strconv.FormatInt(res.Stat.Size, 10))
	w.WriteHeader(http.StatusOK)
	// Chunked copy with an early-cancel check per MiB.
	buf := make([]byte, 1<<20)
	var sent int64
	for sent < res.Stat.Size {
		select {
		case <-r.Context().Done():
			return
		default:
		}
		nr, er := resolver.ReadFD(fd, buf)
		if nr > 0 {
			if _, ew := w.Write(buf[:nr]); ew != nil {
				return
			}
			sent += int64(nr)
		}
		if er != nil {
			return // EOF or read error ends the stream
		}
	}
}

func urlEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func (d Deps) handleSearch(w http.ResponseWriter, r *http.Request) {
	ref, err := parseRootQ(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	base := r.URL.Query().Get("path")
	q := strings.ToLower(r.URL.Query().Get("q"))
	if q == "" {
		writeErr(w, troveerr.New(troveerr.BadRequest, "empty query"))
		return
	}
	maxResults, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if maxResults <= 0 || maxResults > 2000 {
		maxResults = 500
	}
	root, err := d.Resolver.RootOf(ref)
	if err != nil {
		writeErr(w, err)
		return
	}

	type hit struct {
		Path string    `json:"path"`
		Kind string    `json:"kind"`
		Size int64     `json:"size"`
		Time time.Time `json:"mtime"`
	}
	hits := []hit{}
	scanned := 0
	truncated := false
	_, err = d.Resolver.Walk(root, base, func(rel string, st *resolver.StatInfo) error {
		scanned++
		if scanned > 20000 || len(hits) >= maxResults {
			truncated = true
			return troveerr.New(troveerr.Internal, "stop walk") // sentinel below
		}
		if strings.Contains(strings.ToLower(path.Base("/"+rel)), q) {
			hits = append(hits, hit{
				Path: rel, Kind: string(st.Kind), Size: st.Size,
				Time: time.Unix(st.Mtime, int64(st.MtimeN)).UTC(),
			})
		}
		return nil
	}, resolver.WalkCaps{MaxEntries: 20001, MaxDepth: 32, TimeBudgetN: 8e9})
	if err != nil && troveerr.Of(err) != troveerr.Internal {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"root": ref, "query": q, "scanned": scanned, "truncated": truncated, "hits": hits,
	})
}

// handlePlan runs the dry-run and returns plan + token (or needsAck).
func (d Deps) handlePlan(w http.ResponseWriter, r *http.Request) {
	var req model.PlanRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	plan, _, err := d.Planner.Plan(req)
	if err != nil {
		// plans may carry NeedsAck without error — distinguish
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

// handleExecute runs instant actions (mkdir, rename).
func (d Deps) handleExecute(w http.ResponseWriter, r *http.Request) {
	if d.Executor == nil {
		writeErr(w, troveerr.New(troveerr.JobStorageUnavailable, readOnlyWhy(d.Cfg())))
		return
	}
	var body struct {
		PlanID   string `json:"planId"`
		Token    string `json:"token"`
		Decision string `json:"decision"` // "" | skip | overwrite
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	man, err := d.Planner.Consume(body.PlanID, body.Token)
	if err != nil {
		writeErr(w, err)
		return
	}
	switch man.Action {
	case model.ActMkdir:
		err = d.Executor.Mkdir(man)
	case model.ActRename:
		err = d.Executor.Rename(man, executor.RenameDecision(body.Decision))
	default:
		err = troveerr.New(troveerr.BadRequest, "action requires a background job")
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleJobSubmit queues a background job from an approved plan.
func (d Deps) handleJobSubmit(w http.ResponseWriter, r *http.Request) {
	if d.Manager == nil {
		writeErr(w, troveerr.New(troveerr.JobStorageUnavailable, readOnlyWhy(d.Cfg())))
		return
	}
	var body struct {
		PlanID          string `json:"planId"`
		Token           string `json:"token"`
		ClientRequestID string `json:"clientRequestId"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	man, err := d.Planner.Consume(body.PlanID, body.Token)
	if err != nil {
		writeErr(w, err)
		return
	}
	// Keep retry metadata in the job record: source/target/targets survive
	// daemon restarts so a retry can re-plan safely.
	jobPlan := &model.Plan{
		ID:         body.PlanID,
		Action:     man.Action,
		Policy:     man.Policy,
		TargetName: man.TargetName,
	}
	if man.SourceRoot.ID != "" {
		jobPlan.Source = &model.Node{Root: man.SourceRoot, Path: man.SourceRel}
	}
	if man.TargetRoot.ID != "" {
		jobPlan.TargetDir = &model.Node{Root: man.TargetRoot, Path: man.TargetDir}
	}
	jobPlan.Targets = man.DeleteTargets
	job, err := d.Manager.Submit(man, jobPlan, body.ClientRequestID)
	if err != nil {
		// Duplicate requests still return the existing job for idempotency.
		if troveerr.Of(err) == troveerr.DuplicateRequest && job != nil {
			writeJSON(w, http.StatusOK, job)
			return
		}
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (d Deps) handleJobList(w http.ResponseWriter, r *http.Request) {
	if d.Manager == nil {
		writeJSON(w, http.StatusOK, map[string]any{"jobs": []model.Job{}})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": d.Manager.List(limit)})
}

func (d Deps) handleJobGet(w http.ResponseWriter, r *http.Request) {
	if d.Manager == nil {
		writeErr(w, troveerr.New(troveerr.NotFound, "no jobs in read-only mode"))
		return
	}
	job, err := d.Manager.Get(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (d Deps) handleJobCancel(w http.ResponseWriter, r *http.Request) {
	if d.Manager == nil {
		writeErr(w, troveerr.New(troveerr.NotFound, "no jobs in read-only mode"))
		return
	}
	if err := d.Manager.Cancel(r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelling"})
}

func (d Deps) handleJobRetry(w http.ResponseWriter, r *http.Request) {
	if d.Manager == nil {
		writeErr(w, troveerr.New(troveerr.NotFound, "no jobs in read-only mode"))
		return
	}
	job, err := d.Manager.Retry(r.PathValue("id"), func(req model.PlanRequest) (*planner.Manifest, *model.Plan, error) {
		plan, man, err := d.Planner.Plan(req)
		return man, plan, err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (d Deps) handleJobResolve(w http.ResponseWriter, r *http.Request) {
	if d.Manager == nil {
		writeErr(w, troveerr.New(troveerr.NotFound, "no jobs in read-only mode"))
		return
	}
	var body struct {
		Decisions []struct {
			Path       string `json:"path"`
			Resolution string `json:"resolution"`
		} `json:"decisions"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	m := map[string]string{}
	for _, dec := range body.Decisions {
		m[dec.Path] = dec.Resolution
	}
	if err := d.Manager.Resolve(r.PathValue("id"), m); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (d Deps) handleJobEvents(w http.ResponseWriter, r *http.Request) {
	if d.Manager == nil {
		writeJSON(w, http.StatusOK, map[string]any{"events": []model.Event{}})
		return
	}
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	evs, err := d.Manager.Events(r.PathValue("id"), since, 200)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": evs})
}

// handleDebugLog tails the daemon log (development-preview diagnostic:
// crashes leave their stderr stack traces there; endpoint goes away before
// any stable release).
func (d Deps) handleDebugLog(w http.ResponseWriter, r *http.Request) {
	raw, err := os.ReadFile("/var/log/troved.log")
	if err != nil {
		writeErr(w, troveerr.Wrap(troveerr.IOFailure, err, "read log"))
		return
	}
	log := string(raw)
	if len(log) > 32*1024 {
		log = log[len(log)-32*1024:]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"log":     log,
		"pid":     os.Getpid(),
		"uptimeS": int(time.Since(StartedAt).Seconds()),
	})
}

func (d Deps) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	cfg := d.Cfg()
	writeJSON(w, http.StatusOK, map[string]any{
		"dataDir":       cfg.DataDir,
		"concurrency":   cfg.Concurrency,
		"physicalWrite": cfg.PhysicalWrite,
		"protectedMode": cfg.ProtectedMode,
		"expertMode":    cfg.ExpertMode,
		"showHidden":    cfg.ShowHidden,
		"language":      cfg.Language,
	})
}

func (d Deps) handleSettingsPut(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DataDir       *string `json:"dataDir,omitempty"`
		Concurrency   *int    `json:"concurrency,omitempty"`
		PhysicalWrite *bool   `json:"physicalWrite,omitempty"`
		ProtectedMode *string `json:"protectedMode,omitempty"`
		ExpertMode    *bool   `json:"expertMode,omitempty"`
		ShowHidden    *bool   `json:"showHidden,omitempty"`
		Language      *string `json:"language,omitempty"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	cfg := d.Cfg()
	if body.DataDir != nil {
		p := strings.TrimSpace(*body.DataDir)
		if p != "" && !strings.HasPrefix(p, "/mnt/") {
			writeErr(w, troveerr.New(troveerr.BadRequest,
				"data directory must live under /mnt (array or pool), never on the USB flash"))
			return
		}
		cfg.DataDir = p
	}
	if body.Concurrency != nil {
		cfg.Concurrency = *body.Concurrency
		if cfg.Concurrency < 1 {
			cfg.Concurrency = 1
		}
		if cfg.Concurrency > 4 {
			cfg.Concurrency = 4
		}
	}
	if body.PhysicalWrite != nil {
		cfg.PhysicalWrite = *body.PhysicalWrite
	}
	if body.ProtectedMode != nil {
		if *body.ProtectedMode == "ack" {
			cfg.ProtectedMode = "ack"
		} else {
			cfg.ProtectedMode = "deny"
		}
	}
	if body.ExpertMode != nil {
		cfg.ExpertMode = *body.ExpertMode
	}
	if body.ShowHidden != nil {
		cfg.ShowHidden = *body.ShowHidden
	}
	if body.Language != nil {
		cfg.Language = *body.Language
	}
	if err := cfg.Save(); err != nil {
		writeErr(w, troveerr.Wrap(troveerr.IOFailure, err, "save settings"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
