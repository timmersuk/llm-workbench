package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// fleet.go drives LM Studio's native REST API (/api/v1/*) to sweep every
// downloaded, tool-capable model one at a time: unload the current model,
// load the next with a pinned (small) context, probe it via the ordinary
// OpenAI-compatible surface, unload, repeat. This ties up the whole LM Studio
// instance in exchange for qualifying the entire local fleet without manual
// model switching or holding several large models in memory at once.
//
// This mode is LM-Studio-specific; the rest of modelprobe is provider-
// agnostic. It is inert unless -fleet is passed.

type nativeCaps struct {
	Vision            bool `json:"vision"`
	TrainedForToolUse bool `json:"trained_for_tool_use"`
}

type nativeModel struct {
	Key              string            `json:"key"`
	Type             string            `json:"type"`
	DisplayName      string            `json:"display_name"`
	ParamsString     string            `json:"params_string"`
	MaxContextLength int               `json:"max_context_length"`
	Capabilities     *nativeCaps       `json:"capabilities"` // pointer: null for embeddings
	LoadedInstances  []json.RawMessage `json:"loaded_instances"`
}

type loadResponse struct {
	InstanceID   string  `json:"instance_id"`
	Status       string  `json:"status"`
	LoadTimeSecs float64 `json:"load_time_seconds"`
	Type         string  `json:"type"`
}

// nativeClient talks to LM Studio's /api/v1 management endpoints. It uses a
// generous timeout because a cold model load blocks the HTTP call until the
// model is resident, which can take minutes for a large model on a slow host.
type nativeClient struct {
	baseURL     string // e.g. http://host:1234 (NO /v1 suffix)
	apiKey      string
	http        *http.Client
	loadTimeout time.Duration
}

func newNativeClient(baseURL, apiKey string, loadTimeout time.Duration) *nativeClient {
	// Callers pass the OpenAI-style base (…/v1); the native API is a sibling
	// at /api/v1, so strip a trailing /v1 if present.
	root := strings.TrimRight(baseURL, "/")
	root = strings.TrimSuffix(root, "/v1")
	return &nativeClient{
		baseURL:     root,
		apiKey:      apiKey,
		http:        &http.Client{},
		loadTimeout: loadTimeout,
	}
}

func (n *nativeClient) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, n.baseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if n.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+n.apiKey)
	}
	resp, err := n.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http %d: %s", resp.StatusCode, snippet(raw, 300))
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func (n *nativeClient) listModels(ctx context.Context) ([]nativeModel, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var out struct {
		Models []nativeModel `json:"models"`
	}
	if err := n.do(ctx, http.MethodGet, "/api/v1/models", nil, &out); err != nil {
		return nil, err
	}
	return out.Models, nil
}

// load loads a model by key. When contextLength <= 0 the request omits every
// load parameter, so LM Studio applies the model's saved per-model config —
// the only way to get the user's KV-cache quantization (K=Q8_0/V=Q4_0),
// since the REST load endpoint rejects the quantization fields outright
// (lmstudio-bug-tracker #2024/#107). A positive contextLength overrides the
// saved context but still cannot set cache quantization (forced f16).
func (n *nativeClient) load(ctx context.Context, key string, contextLength int) (loadResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, n.loadTimeout)
	defer cancel()
	body := map[string]any{"model": key}
	if contextLength > 0 {
		body["context_length"] = contextLength
	}
	var out loadResponse
	err := n.do(ctx, http.MethodPost, "/api/v1/models/load", body, &out)
	return out, err
}

func (n *nativeClient) unload(ctx context.Context, instanceID string) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	return n.do(ctx, http.MethodPost, "/api/v1/models/unload", map[string]any{
		"instance_id": instanceID,
	}, nil)
}

// unloadAllLoaded clears every currently-resident instance so the sweep
// starts from a clean slate regardless of the server's auto-evict setting.
// instance_id equals the model key for a singly-loaded model, which is the
// case for every model LM Studio loads here.
func (n *nativeClient) unloadAllLoaded(ctx context.Context, models []nativeModel) {
	for _, m := range models {
		if len(m.LoadedInstances) > 0 {
			if err := n.unload(ctx, m.Key); err != nil {
				fmt.Printf("  (warn: could not unload %s: %v)\n", m.Key, err)
			}
		}
	}
}

// fleetRow is one model's line in the comparison matrix.
type fleetRow struct {
	key      string
	params   string
	loadOK   bool
	loadSecs float64
	loadErr  string
	report   modelReport
}

// runFleet sweeps all tool-capable downloaded models (optionally restricted to
// an allowlist of key substrings) and prints a comparison matrix.
func runFleet(ctx context.Context, oai *client, nat *nativeClient, allow []string, dir, question, expect string, runs, maxTurns, maxTokens, fleetContext int) error {
	models, err := nat.listModels(ctx)
	if err != nil {
		return fmt.Errorf("list models: %w", err)
	}

	targets := make([]nativeModel, 0, len(models))
	for _, m := range models {
		if m.Type != "llm" || m.Capabilities == nil || !m.Capabilities.TrainedForToolUse {
			continue // skip embeddings and non-tool models
		}
		if len(allow) > 0 && !matchesAny(m.Key, allow) {
			continue
		}
		targets = append(targets, m)
	}
	if len(targets) == 0 {
		return fmt.Errorf("no tool-capable models matched (of %d downloaded)", len(models))
	}

	ctxNote := "context: each model's saved LM Studio config (incl. KV-cache quantization)"
	if fleetContext > 0 {
		ctxNote = fmt.Sprintf("context: forced to %d (KV cache f16 — REST cannot set quantization)", fleetContext)
	}
	fmt.Printf("FLEET SWEEP — %d tool-capable model(s), one at a time\n  %s\n", len(targets), ctxNote)
	skipped := len(models) - len(targets)
	if len(allow) == 0 && skipped > 0 {
		fmt.Printf("  (skipped %d non-LLM / non-tool-capable models)\n", skipped)
	}
	fmt.Println("  clearing currently-loaded models…")
	nat.unloadAllLoaded(ctx, models)
	fmt.Println()

	rows := make([]fleetRow, 0, len(targets))
	for i, m := range targets {
		ctxLen := fleetContext
		if ctxLen > 0 && m.MaxContextLength > 0 && ctxLen > m.MaxContextLength {
			ctxLen = m.MaxContextLength
		}
		fmt.Printf("[%d/%d] %s (%s) — loading…", i+1, len(targets), m.Key, m.ParamsString)
		lr, err := nat.load(ctx, m.Key, ctxLen)
		if err != nil {
			fmt.Printf(" LOAD FAILED: %s\n", snippet([]byte(err.Error()), 80))
			rows = append(rows, fleetRow{key: m.Key, params: m.ParamsString, loadErr: err.Error()})
			// Best-effort unload in case it half-loaded.
			_ = nat.unload(ctx, m.Key)
			continue
		}
		fmt.Printf(" loaded in %.1fs, probing…", lr.LoadTimeSecs)
		rep := probeModel(ctx, oai, m.Key, dir, question, expect, runs, maxTurns, maxTokens, false)
		fmt.Printf(" %d/%d ok\n", rep.okCount(), len(rep.runs))
		if err := nat.unload(ctx, lr.InstanceID); err != nil {
			fmt.Printf("  (warn: could not unload %s: %v)\n", lr.InstanceID, err)
		}
		rows = append(rows, fleetRow{key: m.Key, params: m.ParamsString, loadOK: true, loadSecs: lr.LoadTimeSecs, report: rep})
	}

	fmt.Println()
	printMatrix(rows, runs)
	return nil
}

func matchesAny(key string, subs []string) bool {
	for _, s := range subs {
		if strings.Contains(key, s) {
			return true
		}
	}
	return false
}

func printMatrix(rows []fleetRow, runs int) {
	const rowFmt = "  %-48s %-6s %-7s %-10s %-7s %-16s %s\n"
	fmt.Println("FLEET RESULTS")
	fmt.Printf(rowFmt, "MODEL", "PARAMS", "LOAD", "REASON-KEY", "RELIAB", "PATHOLOGIES", "VERDICT")
	for _, r := range rows {
		if !r.loadOK {
			fmt.Printf(rowFmt, truncateLabel(r.key, 48), r.params, "FAIL", "-", "-", "-",
				"LOAD FAILED: "+snippet([]byte(r.loadErr), 50))
			continue
		}
		fmt.Printf(rowFmt,
			truncateLabel(r.key, 48), r.params,
			fmt.Sprintf("%.1fs", r.loadSecs),
			shortReasonKey(r.report.wire.reasoningKey),
			fmt.Sprintf("%d/%d", r.report.okCount(), runs),
			r.report.pathologyLabel(), r.report.verdict())
	}
}

func shortReasonKey(k string) string {
	switch k {
	case "reasoning_content":
		return "reas_cont"
	case "reasoning":
		return "reasoning"
	case "both":
		return "both"
	default:
		return "none"
	}
}
