package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// listModelsTimeout acota la consulta del catalogo de modelos. Es un GET corto
// contra un servidor local; sin deadline, un endpoint colgado dejaria la llamada
// (y la UI que la espera) bloqueada.
const listModelsTimeout = 10 * time.Second

// ListModels consulta el catalogo de modelos de un endpoint OpenAI-compatible
// (GET baseURL/models) y devuelve los ids de la lista `data` en orden. baseURL es la
// misma raiz que recibe el provider (p.ej. http://localhost:1234/v1). apiKey, si no
// esta vacio, viaja como Bearer; los locales (LM Studio, Ollama) no lo exigen. La
// usa la UI para poblar el dropdown de modelos sin que el usuario tipee el id a mano.
func ListModels(ctx context.Context, baseURL, apiKey string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, listModelsTimeout)
	defer cancel()

	url := strings.TrimRight(baseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("listar modelos: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("listar modelos: respuesta invalida: %w", err)
	}

	models := make([]string, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		if m.ID != "" {
			models = append(models, m.ID)
		}
	}
	return models, nil
}
