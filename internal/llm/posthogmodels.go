package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ListPosthogModels asks PostHog's LLM gateway what it serves this account
// (GET baseURL/v1/models with the OAuth bearer) and returns the Claude-family
// ids in order.
//
// It is its own lister rather than a call to [ListModels] for three reasons the
// generic path cannot absorb: the endpoint hangs off /v1 while the provider's
// base URL is the product root, the gateway marks models outside the caller's
// plan with allowed=false — offering those would sell the user a model that
// fails at selection — and this build only speaks the gateway's
// Claude and GPT wire surfaces, so Cloudflare and unknown families it also
// lists are filtered out here rather than failing in the adapter.
func ListPosthogModels(ctx context.Context, baseURL, bearer string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, listModelsTimeout)
	defer cancel()

	url := strings.TrimRight(baseURL, "/") + "/v1/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("list PostHog models: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var parsed struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
			// Allowed is the plan gate: an authenticated fetch marks models outside
			// the caller's plan false. Absence (older gateway) means allowed, which
			// is what a *bool distinguishes from false.
			Allowed *bool `json:"allowed"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("list PostHog models: unreadable response: %w", err)
	}

	models := make([]string, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		if m.ID == "" || (m.Allowed != nil && !*m.Allowed) {
			continue
		}
		claude := m.OwnedBy == "anthropic" && strings.HasPrefix(m.ID, "claude-")
		gpt := m.OwnedBy == "openai" && strings.HasPrefix(m.ID, "gpt-")
		if !claude && !gpt {
			continue
		}
		models = append(models, m.ID)
	}
	return models, nil
}
