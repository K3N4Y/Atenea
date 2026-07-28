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
	return validateBearerKey(ctx, strings.TrimRight(baseURL, "/")+"/key", apiKey)
}

// ValidateOpenAIKey checks an API key with OpenAI's read-only model listing
// before it is stored. The request validates authentication without creating a
// model response or incurring inference usage.
func ValidateOpenAIKey(ctx context.Context, baseURL, apiKey string) error {
	return validateBearerKey(ctx, strings.TrimRight(baseURL, "/")+"/models", apiKey)
}

func validateBearerKey(ctx context.Context, url, apiKey string) error {
	ctx, cancel := context.WithTimeout(ctx, validateKeyTimeout)
	defer cancel()

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
		detail := strings.TrimSpace(string(body))
		if apiKey != "" {
			detail = strings.ReplaceAll(detail, apiKey, "[redacted]")
		}
		return fmt.Errorf("validate key: %s: %s", resp.Status, detail)
	}
}
