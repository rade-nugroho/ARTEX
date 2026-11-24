package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	actool "github.com/Autumn-27/norma/tool"
)

// Ontology tools give workers read access to the auto-ontology semantic layer
// (terms, mappings, joins, reviewed metric SQL) and its text-to-SQL chat. They
// call the sidecar's FastAPI directly server-side, so they bypass the target
// gate — ontology queries are platform calls, not target traffic. Unbound by
// default; attach them to worker agents via the tools table like any host tool.

func (s *Server) ontologyTools() []actool.CoreTool {
	return []actool.CoreTool{
		s.toolOntologyStatus(),
		s.toolOntologySearch(),
		s.toolOntologyTerm(),
		s.toolOntologyQuery(),
	}
}

// ontologyCall performs one request against the sidecar and returns the parsed
// JSON body (or raw text for non-JSON). timeout bounds the whole call.
func (s *Server) ontologyCall(ctx context.Context, method, path string, body any, timeout time.Duration) (int, json.RawMessage, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.ontologyBase()+"/api"+path, rdr)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c := &http.Client{Timeout: timeout}
	resp, err := c.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, json.RawMessage(raw), nil
}

func (s *Server) toolOntologyStatus() actool.CoreTool {
	return wrTool("ontology_status",
		"Check the auto-ontology sidecar: reachability, catalog databases, and semantic-compilation status. Use before ontology_search/ontology_term/ontology_query; if unreachable or not compiled, tell the operator to start the sidecar and compile the semantic layer.",
		objSchema(map[string]any{}),
		func(ctx context.Context, _ json.RawMessage) (actool.Result, error) {
			code, raw, err := s.ontologyCall(ctx, http.MethodGet, "/semantic-compilation/status", nil, 10*time.Second)
			if err != nil {
				return actool.Errorf(fmt.Sprintf("ontology sidecar unreachable at %s: %v", s.ontologyBase(), err)), nil
			}
			var status any
			_ = json.Unmarshal(raw, &status)
			code2, raw2, err2 := s.ontologyCall(ctx, http.MethodGet, "/datasources/dbs", nil, 10*time.Second)
			dbs := any(nil)
			if err2 == nil && code2 == 200 {
				_ = json.Unmarshal(raw2, &dbs)
			}
			return jsonResult(map[string]any{
				"semantic_status": status,
				"http_status":     code,
				"databases":       dbs,
			})
		})
}

func (s *Server) toolOntologySearch() actool.CoreTool {
	return wrTool("ontology_search",
		"Full-text search across the auto-ontology catalog and semantic layer (terms, tables, columns). Returns ranked hits with ids usable by ontology_term. Requires >=2 chars.",
		objSchema(map[string]any{
			"query": strParam("Search text, e.g. a business term like 'monthly recurring revenue' or a table/column name"),
		}, "query"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Query string `json:"query"`
			}
			_ = json.Unmarshal(in, &a)
			if strings.TrimSpace(a.Query) == "" {
				return actool.Errorf("query must not be empty"), nil
			}
			code, raw, err := s.ontologyCall(ctx, http.MethodPost, "/search/global-search",
				map[string]any{"search_term": a.Query}, 30*time.Second)
			if err != nil {
				return actool.Errorf(fmt.Sprintf("ontology search failed: %v", err)), nil
			}
			if code != 200 {
				return actool.Errorf(fmt.Sprintf("ontology search http %d: %s", code, truncate(string(raw), 500))), nil
			}
			return actool.Text(truncate(string(raw), 8000)), nil
		})
}

func (s *Server) toolOntologyTerm() actool.CoreTool {
	return wrTool("ontology_term",
		"Fetch one ontology term by id (from ontology_search): its definition, mapped columns, SQL attributes (reviewed metric queries), and relationships. term_id is the id field of a search hit.",
		objSchema(map[string]any{
			"term_id": strParam("Term node id from an ontology_search hit"),
		}, "term_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				TermID string `json:"term_id"`
			}
			_ = json.Unmarshal(in, &a)
			if strings.TrimSpace(a.TermID) == "" {
				return actool.Errorf("term_id must not be empty"), nil
			}
			id := strings.TrimSpace(a.TermID)
			var b strings.Builder
			for _, p := range []string{
				"/terms/" + id,
				"/terms/" + id + "/sql-attributes",
				"/terms/" + id + "/column-attributes",
			} {
				code, raw, err := s.ontologyCall(ctx, http.MethodGet, p, nil, 15*time.Second)
				if err != nil {
					return actool.Errorf(fmt.Sprintf("ontology term fetch failed: %v", err)), nil
				}
				if code != 200 {
					b.WriteString("\n--- " + p + " -> http " + fmt.Sprint(code) + " ---\n")
					continue
				}
				b.WriteString("\n--- " + p + " ---\n" + truncate(string(raw), 12000) + "\n")
			}
			return actool.Text(b.String()), nil
		})
}

func (s *Server) toolOntologyQuery() actool.CoreTool {
	return wrTool("ontology_query",
		"Ask the auto-ontology text-to-SQL agent a business question about a connected data source. Returns the agent's answer plus the validated SQL it ran. Requires the semantic layer to be compiled (ontology_status). Long-running: allow up to ~3 minutes.",
		objSchema(map[string]any{
			"question": strParam("Natural-language question about the connected data, e.g. 'revenue by region last quarter'"),
			"target_db": map[string]any{
				"type":        "string",
				"description": "Optional catalog database name/UUID to scope the query",
			},
		}, "question"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Question string `json:"question"`
				TargetDB string `json:"target_db"`
			}
			_ = json.Unmarshal(in, &a)
			if strings.TrimSpace(a.Question) == "" {
				return actool.Errorf("question must not be empty"), nil
			}
			payload := map[string]any{"question": a.Question}
			if strings.TrimSpace(a.TargetDB) != "" {
				payload["target_db"] = a.TargetDB
			}
			body, _ := json.Marshal(payload)
			req, err := http.NewRequestWithContext(ctx, http.MethodPost,
				s.ontologyBase()+"/api/chat/completions", bytes.NewReader(body))
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			req.Header.Set("Content-Type", "application/json")
			c := &http.Client{Timeout: 4 * time.Minute}
			resp, err := c.Do(req)
			if err != nil {
				return actool.Errorf(fmt.Sprintf("ontology query failed: %v", err)), nil
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
				return actool.Errorf(fmt.Sprintf("ontology query http %d: %s", resp.StatusCode, truncate(string(raw), 800))), nil
			}
			return actool.Text(s.readOntologySSE(resp.Body)), nil
		})
}

// readOntologySSE consumes the chat/completions event stream and returns a
// compact summary: the final result event's answer (response + sql_code), plus
// any error event. Intermediate step/sql events are dropped to keep tool output
// small; the full stream stays available via the UI proxy.
func (s *Server) readOntologySSE(r io.Reader) string {
	var answer, sqlCode, errMsg string
	var steps []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var ev map[string]any
		if json.Unmarshal([]byte(data), &ev) != nil {
			continue
		}
		switch ev["type"] {
		case "step":
			if l, ok := ev["label"].(string); ok && l != "" {
				steps = append(steps, l)
			}
		case "result":
			if ans, ok := ev["answer"].(map[string]any); ok {
				answer, _ = ans["response"].(string)
				sqlCode, _ = ans["sql_code"].(string)
			}
		case "error":
			errMsg, _ = ev["message"].(string)
		}
	}
	var b strings.Builder
	if errMsg != "" {
		b.WriteString("ERROR: " + errMsg + "\n")
	}
	if len(steps) > 0 {
		b.WriteString("Steps: " + strings.Join(steps, " → ") + "\n")
	}
	if sqlCode != "" {
		b.WriteString("SQL:\n" + sqlCode + "\n")
	}
	if answer != "" {
		b.WriteString("Answer: " + answer + "\n")
	}
	if b.Len() == 0 {
		return "(no result event received; stream ended early)"
	}
	return b.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
