package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// TypeSafe integration is entirely optional: if OIDO_TYPESAFE_API_KEY is unset,
// typesafeConfigFromEnv returns nil and RunMCPServer skips registering the
// tools in this file. No other Gmail tool depends on this file.

type typesafeConfig struct {
	apiKey  string
	baseURL string
	model   string
}

func typesafeConfigFromEnv() *typesafeConfig {
	apiKey := os.Getenv("OIDO_TYPESAFE_API_KEY")
	if apiKey == "" {
		return nil
	}
	baseURL := os.Getenv("OIDO_TYPESAFE_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.typesafe.ai/v1"
	}
	model := os.Getenv("OIDO_TYPESAFE_MODEL")
	if model == "" {
		model = "jev-latest"
	}
	return &typesafeConfig{apiKey: apiKey, baseURL: baseURL, model: model}
}

// typesafeQuestion mirrors one entry of the TypeSafe /systemone "questions" map.
type typesafeQuestion struct {
	Type         string `json:"type"`
	Instructions any    `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

type typesafeAnswer struct {
	Type          string             `json:"type"`
	Noul          *float64           `json:"noul,omitempty"`
	Choice        string             `json:"choice,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
}

type typesafeClient struct {
	cfg *typesafeConfig
	hc  *http.Client
}

func newTypesafeClient(cfg *typesafeConfig) *typesafeClient {
	// gmail_digest can pack up to 150 questions (count=50 x 3) into one
	// request; give it more headroom than a single-question call needs.
	return &typesafeClient{cfg: cfg, hc: &http.Client{Timeout: 60 * time.Second}}
}

// ask sends parallel questions over one shared state in a single request, so
// scoring/classifying N emails costs one HTTP round trip, not N.
func (t *typesafeClient) ask(ctx context.Context, state any, questions map[string]typesafeQuestion) (map[string]typesafeAnswer, error) {
	payload, err := json.Marshal(map[string]any{
		"state":     state,
		"model":     t.cfg.model,
		"questions": questions,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal typesafe request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.cfg.baseURL+"/systemone", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build typesafe request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+t.cfg.apiKey)

	resp, err := t.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("typesafe request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read typesafe response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("typesafe api error (status %d): %s", resp.StatusCode, string(body))
	}

	var out struct {
		Answers map[string]typesafeAnswer `json:"answers"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parse typesafe response: %w", err)
	}
	return out.Answers, nil
}
