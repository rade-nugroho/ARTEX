package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
)

// Auto Ontology (NVIDIA/auto-ontology) integration. The Python stack runs as a
// local sidecar: FastAPI on :3001 (unauthenticated — keep it host-local), the
// ingestion service on :3002. This file proxies a read/query whitelist of its
// REST API, reports status, and renders/writes the .env fragment that points
// its model triplets at an ARTEX LLM profile (any OpenAI-compatible endpoint;
// no NVIDIA NIM required).

const (
	settingOntologyURL        = "ontology_url"         // FastAPI base, default http://127.0.0.1:3001
	settingOntologyEnvPath    = "ontology_env_path"    // auto-ontology/.env to manage; empty = fragment only
	settingOntologyEmbedModel = "ontology_embed_model" // embedding model for EMBED_*; per-format default when empty
	defaultOntologyURL        = "http://127.0.0.1:3001"
	ontologyEnvBegin          = "# >>> artex-ontology (managed) >>>"
	ontologyEnvEnd            = "# <<< artex-ontology <<<"
)

func (s *Server) ontologyBase() string {
	if v, ok, _ := s.m.pg.GetSetting(settingOntologyURL); ok && strings.TrimSpace(v) != "" {
		return strings.TrimRight(strings.TrimSpace(v), "/")
	}
	return defaultOntologyURL
}

// ontologyConfig is GET/PUT /api/ontology/config.
type ontologyConfig struct {
	URL        string `json:"url"`
	EnvPath    string `json:"env_path"`
	EnvPathSet bool   `json:"env_path_set"`
	EmbedModel string `json:"embed_model"`
	Reachable  bool   `json:"reachable"`
}

func (s *Server) pgOntologyConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg := ontologyConfig{
			URL:        s.ontologyBase(),
			EmbedModel: s.ontologyEmbedModel(),
		}
		if v, ok, _ := s.m.pg.GetSetting(settingOntologyEnvPath); ok {
			cfg.EnvPath, cfg.EnvPathSet = v, v != ""
		}
		cfg.Reachable = s.ontologyReachable(r.Context())
		writeJSON(w, 200, cfg)
	case http.MethodPut:
		var in struct {
			URL        *string `json:"url"`
			EnvPath    *string `json:"env_path"`
			EmbedModel *string `json:"embed_model"`
		}
		if err := decode(w, r, &in); err != nil && err != io.EOF {
			writeErr(w, 400, err.Error())
			return
		}
		if in.URL != nil {
			u := strings.TrimRight(strings.TrimSpace(*in.URL), "/")
			if u != "" {
				if _, err := url.Parse(u); err != nil || !strings.HasPrefix(u, "http") {
					writeErr(w, 400, "url must be an http(s) base URL")
					return
				}
			}
			if err := s.m.pg.SetSetting(settingOntologyURL, u); err != nil {
				writeErr(w, 500, err.Error())
				return
			}
		}
		if in.EnvPath != nil {
			if err := s.m.pg.SetSetting(settingOntologyEnvPath, strings.TrimSpace(*in.EnvPath)); err != nil {
				writeErr(w, 500, err.Error())
				return
			}
		}
		if in.EmbedModel != nil {
			if err := s.m.pg.SetSetting(settingOntologyEmbedModel, strings.TrimSpace(*in.EmbedModel)); err != nil {
				writeErr(w, 500, err.Error())
				return
			}
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *Server) ontologyEmbedModel() string {
	if v, ok, _ := s.m.pg.GetSetting(settingOntologyEmbedModel); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// ontologyReachable pings the FastAPI datasources endpoint with a short timeout.
func (s *Server) ontologyReachable(ctx context.Context) bool {
	c := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.ontologyBase()+"/api/datasources/dbs", nil)
	if err != nil {
		return false
	}
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

// pgOntologyStatus → GET /api/ontology/status: reachability + semantic compile status.
func (s *Server) pgOntologyStatus(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"url": s.ontologyBase(), "reachable": false}
	c := &http.Client{Timeout: 5 * time.Second}
	for _, p := range []string{"/api/datasources/dbs", "/api/semantic-compilation/status"} {
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, s.ontologyBase()+p, nil)
		if err != nil {
			continue
		}
		resp, err := c.Do(req)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode == 200 {
			out["reachable"] = true
			var v any
			if json.Unmarshal(body, &v) == nil {
				out[strings.TrimPrefix(p, "/api/")] = v
			}
		}
	}
	writeJSON(w, 200, out)
}

// ontologyProxyAllowed is the whitelist for GET /api/ontology/proxy/{path...}.
// Read endpoints are prefix-matched; the few POST endpoints are exact matches.
// Writes to the ontology (terms/sql-attributes/connections CRUD) stay on the
// auto-ontology UI — ARTEX only queries.
var ontologyProxyGetPrefixes = []string{
	"/api/datasources",
	"/api/connections",
	"/api/semantic-compilation/status",
	"/api/terms",
	"/api/sql-attributes",
	"/api/tables",
	"/api/columns",
	"/api/schemas",
	"/api/exploration",
	"/api/chat/watch",
}

var ontologyProxyPostExact = map[string]bool{
	"/api/chat/completions":             true,
	"/api/chat/cancel":                  true,
	"/api/search/global-search":         true,
	"/api/search/global-search/count":   true,
	"/api/question-entity-coverage":     true,
	"/api/semantic-compilation/trigger": true,
	"/api/semantic-compilation/reset":   true,
	"/api/connections/test":             true,
}

func ontologyProxyAllowed(method, upstreamPath string) bool {
	if method == http.MethodGet || method == http.MethodHead {
		for _, p := range ontologyProxyGetPrefixes {
			if upstreamPath == p || strings.HasPrefix(upstreamPath, p+"/") {
				return true
			}
		}
		return false
	}
	if method == http.MethodPost {
		return ontologyProxyPostExact[upstreamPath]
	}
	return false
}

// ontologyProxy forwards /api/ontology/proxy/{path...} to {base}/api/{path}.
// SSE endpoints (chat/completions, chat/watch) stream through unchanged.
func (s *Server) ontologyProxy(w http.ResponseWriter, r *http.Request) {
	rel := "/" + strings.TrimPrefix(r.PathValue("path"), "/")
	upstreamPath := "/api" + rel
	if !ontologyProxyAllowed(r.Method, upstreamPath) {
		writeErr(w, 403, "ontology proxy: "+r.Method+" "+upstreamPath+" is not allowed")
		return
	}
	target, err := url.Parse(s.ontologyBase())
	if err != nil {
		writeErr(w, 500, "bad ontology url")
		return
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = upstreamPath
			pr.Out.URL.RawPath = ""
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			writeErr(w, 502, "ontology upstream unreachable: "+err.Error())
		},
	}
	proxy.ServeHTTP(w, r)
}

// ---- LLM env export ----

// ontologyLLMEnv renders the auto-ontology model-triplet env fragment for an
// ARTEX LLM profile. Anthropic-native profiles cannot map (auto-ontology only
// speaks OpenAI-compatible chat/embeddings), so they return an error.
func (s *Server) ontologyLLMEnv(profileID int64) (string, string, error) {
	var p *db.LLMProfile
	var err error
	if profileID > 0 {
		p, err = s.m.pg.ProfileByID(profileID)
	} else {
		p, err = s.m.pg.ActiveProfile()
	}
	if err != nil {
		return "", "", fmt.Errorf("load profile: %w", err)
	}
	if p == nil {
		return "", "", fmt.Errorf("no LLM profile configured in ARTEX")
	}
	format := strings.ToLower(strings.TrimSpace(p.Format))
	endpoint := strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
	apiKey := p.APIKey
	model := strings.TrimSpace(p.Model)

	switch format {
	case "openai", "openai-responses":
		if endpoint == "" {
			endpoint = "https://api.openai.com/v1"
		}
	case "ollama":
		if endpoint == "" {
			endpoint = "http://127.0.0.1:11434/v1"
		}
		if apiKey == "" {
			apiKey = "ollama"
		}
	default:
		return "", "", fmt.Errorf("profile %q uses format %q; auto-ontology needs an OpenAI-compatible endpoint (openai/openai-responses/ollama)", p.Name, p.Format)
	}
	if model == "" {
		return "", "", fmt.Errorf("profile %q has no model set", p.Name)
	}
	if strings.HasPrefix(model, "ollama/") {
		return "", "", fmt.Errorf("model %q would route to Ollama Cloud; rename it without the ollama/ prefix to hit REASONING_ENDPOINT", model)
	}

	embedModel := s.ontologyEmbedModel()
	if embedModel == "" {
		if format == "ollama" {
			embedModel = "nomic-embed-text"
		} else {
			embedModel = "text-embedding-3-small"
		}
	}

	var b strings.Builder
	b.WriteString("# Generated by ARTEX from LLM profile \"" + p.Name + "\" (" + format + ")\n")
	b.WriteString(ontologyEnvBegin + "\n")
	for _, kv := range [][2]string{
		{"REASONING_ENDPOINT", endpoint},
		{"REASONING_API_KEY", apiKey},
		{"REASONING_MODEL", model},
		{"NON_REASONING_ENDPOINT", endpoint},
		{"NON_REASONING_API_KEY", apiKey},
		{"NON_REASONING_MODEL", model},
		{"EMBED_ENDPOINT", endpoint},
		{"EMBED_API_KEY", apiKey},
		{"EMBED_MODEL", embedModel},
	} {
		b.WriteString(kv[0] + "=" + kv[1] + "\n")
	}
	b.WriteString("# RERANK_* is NVIDIA-NIM-only; auto-ontology degrades gracefully without it.\n")
	b.WriteString(ontologyEnvEnd + "\n")
	return b.String(), fmt.Sprintf("profile %q (%s / %s)", p.Name, format, model), nil
}

// pgOntologyLLMEnv → GET /api/ontology/llm-env?profile_id=
func (s *Server) pgOntologyLLMEnv(w http.ResponseWriter, r *http.Request) {
	var pid int64
	if v := r.URL.Query().Get("profile_id"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeErr(w, 400, "bad profile_id")
			return
		}
		pid = n
	}
	env, desc, err := s.ontologyLLMEnv(pid)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"env": env, "source": desc})
}

// pgOntologyEnvFile → POST /api/ontology/env-file: write the managed block into
// the configured auto-ontology .env (setting ontology_env_path, or body path).
func (s *Server) pgOntologyEnvFile(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Path *string `json:"path"`
	}
	_ = decode(w, r, &in) // empty body is fine
	path := ""
	if in.Path != nil {
		path = strings.TrimSpace(*in.Path)
	}
	if path == "" {
		if v, ok, _ := s.m.pg.GetSetting(settingOntologyEnvPath); ok {
			path = strings.TrimSpace(v)
		}
	}
	if path == "" {
		writeErr(w, 400, "no env path: set ontology_env_path in config or pass path")
		return
	}
	env, desc, err := s.ontologyLLMEnv(0)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	merged, err := mergeEnvBlock(path, env)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := os.WriteFile(path, []byte(merged), 0o600); err != nil {
		writeErr(w, 500, "write "+path+": "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"written": filepath.Clean(path), "source": desc})
}

// mergeEnvBlock replaces the managed block in an existing .env, or appends it.
func mergeEnvBlock(path, block string) (string, error) {
	var existing string
	if b, err := os.ReadFile(path); err == nil {
		existing = string(b)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	begin := strings.Index(existing, ontologyEnvBegin)
	end := strings.Index(existing, ontologyEnvEnd)
	if begin >= 0 && end > begin {
		tail := existing[end+len(ontologyEnvEnd):]
		if tail != "" && !strings.HasPrefix(tail, "\n") {
			tail = "\n" + tail
		}
		return existing[:begin] + block + tail, nil
	}
	sep := ""
	if existing != "" && !strings.HasSuffix(existing, "\n") {
		sep = "\n"
	}
	return existing + sep + "\n" + block, nil
}
