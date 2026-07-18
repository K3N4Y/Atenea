package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// validateKeyTimeout bounds the key check so /connect never leaves the UI
// waiting on a hung endpoint.
const validateKeyTimeout = 10 * time.Second

// ValidateOpenRouterKey checks an API key against OpenRouter's key endpoint
// (GET baseURL/key) before it gets stored: a key that fails here would only
// explode later, mid-chat, with a much more confusing error. A 401/403 means
// the key is wrong; any other non-200 is surfaced as-is so network or gateway
// trouble is distinguishable from a bad key.
func ValidateOpenRouterKey(ctx context.Context, baseURL, apiKey string) error {
	ctx, cancel := context.WithTimeout(ctx, validateKeyTimeout)
	defer cancel()

	url := strings.TrimRight(baseURL, "/") + "/key"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return errors.New("invalid API key")
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("validate key: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
}
