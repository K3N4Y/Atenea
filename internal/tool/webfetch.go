package tool

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"atenea/internal/llm"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
)

// Limites y defaults de la tool web_fetch. El tope de tamano corta paginas
// enormes antes de leerlas (igual que opencode: 5MB); el timeout default acota la
// espera del GET. maxSize es un campo (no const) para que los tests lo bajen sin
// servir megabytes. El User-Agent imita un navegador real porque muchos sitios
// responden 403 a clientes sin U="navegador".
const (
	maxWebFetchSize        = 5 * 1024 * 1024 // 5MB
	defaultWebFetchTimeout = 30 * time.Second
	webFetchUserAgent      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36"
	webFetchAccept         = "text/html,application/xhtml+xml,application/xml;q=0.9,text/plain;q=0.8,*/*;q=0.7"
)

// WebFetchTool baja una URL, la convierte a markdown y destila el contenido
// respondiendo el prompt con un modelo barato (estilo Claude Code), en vez de
// volcar el HTML crudo al contexto. Es de solo lectura. provider corre el
// destilado (una completion one-shot via Stream); client y blockIP y maxSize son
// inyectables para que los tests no toquen la red real ni dependan de DNS: el
// camino feliz apunta a un httptest.Server (loopback) con un guard permisivo,
// mientras que el guard real veda hosts privados/locales (SSRF).
type WebFetchTool struct {
	provider llm.Provider
	client   *http.Client
	// blockIP decide si una IP resuelta esta vedada (SSRF). nil => todo permitido.
	blockIP func(net.IP) bool
	// maxSize es el tope de bytes del cuerpo; 0 => maxWebFetchSize.
	maxSize int64
}

// maxWebFetchRedirects acota la cadena de redirects que el cliente sigue antes de
// rendirse, igual que el default de net/http (10), para no quedar en un loop.
const maxWebFetchRedirects = 10

// NewWebFetchTool arma la tool con el provider para el destilado y los defaults de
// red: un http.Client con timeout y el guard SSRF que veda loopback/privadas.
func NewWebFetchTool(provider llm.Provider) *WebFetchTool {
	wf := &WebFetchTool{
		provider: provider,
		blockIP:  isPrivateOrLoopback,
		maxSize:  maxWebFetchSize,
	}
	wf.setClient(&http.Client{Timeout: defaultWebFetchTimeout})
	return wf
}

// setClient instala el http.Client y le fija el guard de redirects UNA vez, de modo
// que fetch nunca mute el client compartido (eso causaba una data race cuando el
// runner ejecuta dos web_fetch concurrentes sobre la misma instancia). El guard es
// un closure que lee wf.checkSSRF en cada salto, asi que sigue siendo per-instance.
// Los tests que reemplazan el client deben usar este helper para conservar el guard.
func (wf *WebFetchTool) setClient(c *http.Client) {
	// Re-validar SSRF en CADA salto de redirect: el checkSSRF previo solo cubre el
	// host inicial, asi que sin esto una URL publica podria redirigir (302) a
	// 169.254.169.254 / 127.0.0.1 y el cliente la alcanzaria (bypass / DNS-rebinding).
	// Un CheckRedirect custom desactiva el cap interno de Go, asi que lo reimplementamos.
	c.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) >= maxWebFetchRedirects {
			return fmt.Errorf("web_fetch: demasiados redirects (> %d)", maxWebFetchRedirects)
		}
		return wf.checkSSRF(r.Context(), r.URL.Hostname())
	}
	wf.client = c
}

func (*WebFetchTool) Name() string { return "web_fetch" }

//go:embed webfetch.txt
var webFetchDescription string

func (*WebFetchTool) Description() string { return webFetchDescription }

func (*WebFetchTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","description":"URL http(s) a traer."},"prompt":{"type":"string","description":"Que extraer o responder a partir del contenido de la pagina."}},"required":["url","prompt"]}`)
}

// Execute parsea {url, prompt}, normaliza y valida la URL (upgrade http->https),
// veda hosts privados/locales (SSRF), trae el cuerpo (tope de tamano), lo
// convierte a markdown si es HTML y destila la respuesta al prompt con el modelo.
// Devuelve solo la respuesta destilada, no el HTML crudo.
func (wf *WebFetchTool) Execute(ctx context.Context, input json.RawMessage) (Result, error) {
	var in struct {
		URL    string `json:"url"`
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{}, fmt.Errorf("web_fetch: input invalido: %w", err)
	}
	if strings.TrimSpace(in.URL) == "" {
		return Result{}, fmt.Errorf("web_fetch: url requerida")
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return Result{}, fmt.Errorf("web_fetch: prompt requerido")
	}

	u, err := normalizeWebFetchURL(in.URL)
	if err != nil {
		return Result{}, err
	}
	if err := wf.checkSSRF(ctx, u.Hostname()); err != nil {
		return Result{}, err
	}

	body, contentType, err := wf.fetch(ctx, u.String())
	if err != nil {
		return Result{}, err
	}

	content := body
	if strings.Contains(strings.ToLower(contentType), "text/html") {
		if md, convErr := htmltomarkdown.ConvertString(body); convErr == nil {
			content = md
		}
	}

	answer, err := wf.distill(ctx, in.Prompt, u.String(), content)
	if err != nil {
		return Result{}, err
	}
	return Result{Output: answer}, nil
}

// normalizeWebFetchURL parsea la URL, actualiza http a https (igual que Claude
// Code: nunca trafico en claro) y rechaza cualquier esquema que no sea https.
func normalizeWebFetchURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("web_fetch: url invalida: %w", err)
	}
	if u.Scheme == "http" {
		u.Scheme = "https"
	}
	if u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("web_fetch: la url debe ser http(s) con host")
	}
	return u, nil
}

// checkSSRF resuelve el host a IPs y rechaza si alguna esta vedada por blockIP
// (loopback, privada, link-local: cubre 127.0.0.0/8, 10/8, 172.16/12, 192.168/16,
// 169.254/16 -incl. el endpoint de metadata de la nube- y sus equivalentes IPv6).
// Una IP literal no toca DNS. blockIP nil deja pasar todo (tests).
func (wf *WebFetchTool) checkSSRF(ctx context.Context, host string) error {
	if wf.blockIP == nil {
		return nil
	}
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return fmt.Errorf("web_fetch: no se pudo resolver %q: %w", host, err)
		}
		for _, a := range addrs {
			ips = append(ips, a.IP)
		}
	}
	for _, ip := range ips {
		if wf.blockIP(ip) {
			return fmt.Errorf("web_fetch: acceso bloqueado a host privado/local (%s)", ip)
		}
	}
	return nil
}

// isPrivateOrLoopback es el guard SSRF por defecto: veda las IPs que no deberian
// alcanzarse desde una URL elegida por el modelo.
func isPrivateOrLoopback(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified()
}

// fetch hace el GET con headers de navegador y devuelve el cuerpo (acotado al
// tope de tamano) y el Content-Type. Un status fuera de 2xx es error. El tope se
// chequea por el header Content-Length y, como ese header miente o falta, tambien
// por los bytes realmente leidos (io.LimitReader con un byte extra de margen).
func (wf *WebFetchTool) fetch(ctx context.Context, rawURL string) (body, contentType string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", "", fmt.Errorf("web_fetch: %w", err)
	}
	req.Header.Set("User-Agent", webFetchUserAgent)
	req.Header.Set("Accept", webFetchAccept)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	// El guard de redirects (re-valida SSRF en cada salto y reimplementa el cap) se
	// instala una sola vez en setClient, no aca: mutar wf.client por-fetch corre con
	// http.Client.Do cuando el runner ejecuta dos web_fetch en paralelo (data race).
	resp, err := wf.client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("web_fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("web_fetch: %s devolvio %s", rawURL, resp.Status)
	}

	limit := wf.maxSize
	if limit <= 0 {
		limit = maxWebFetchSize
	}
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		if n, perr := strconv.ParseInt(cl, 10, 64); perr == nil && n > limit {
			return "", "", fmt.Errorf("web_fetch: respuesta demasiado grande (> %d bytes)", limit)
		}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return "", "", fmt.Errorf("web_fetch: %w", err)
	}
	if int64(len(raw)) > limit {
		return "", "", fmt.Errorf("web_fetch: respuesta demasiado grande (> %d bytes)", limit)
	}
	return string(raw), resp.Header.Get("Content-Type"), nil
}

// webFetchSystemPrompt instruye al modelo a responder SOLO con el contenido dado,
// sin inventar, para que el destilado no aluciname mas alla de la pagina.
const webFetchSystemPrompt = "Sos un extractor de contenido web. Te paso el contenido de una pagina (en markdown) y una instruccion. Responde la instruccion de forma concisa usando UNICAMENTE ese contenido. Si la respuesta no esta en la pagina, decilo claramente. No inventes."

// distill corre una completion one-shot con el provider (system de extraccion +
// el contenido y el prompt como mensaje de usuario) y junta el texto emitido. No
// arma un runner ni tools: solo necesita el texto de un turno.
func (wf *WebFetchTool) distill(ctx context.Context, prompt, source, content string) (string, error) {
	user := fmt.Sprintf("Instruccion: %s\n\nFuente: %s\n\nContenido:\n%s", prompt, source, content)
	out, err := wf.provider.Stream(ctx, llm.Request{
		System:   webFetchSystemPrompt,
		Messages: []llm.Message{{Role: "user", Text: user}},
	})
	if err != nil {
		return "", fmt.Errorf("web_fetch: destilado fallo: %w", err)
	}
	var b strings.Builder
	for ev := range out {
		if ev.Kind == llm.TextDelta {
			b.WriteString(ev.Text)
		}
	}
	return strings.TrimSpace(b.String()), nil
}

// var _ tool.Tool = (*WebFetchTool)(nil) asegura en compilacion que cumple la interface.
var _ Tool = (*WebFetchTool)(nil)
