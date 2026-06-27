package terminal

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// collector junta los chunks de onData de forma segura y permite esperar a que la
// salida acumulada contenga un substring (o reventar por timeout). El read corre
// en una goroutine, asi que el test necesita esperar, no leer una vez.
type collector struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (c *collector) write(b []byte) {
	c.mu.Lock()
	c.buf.Write(b)
	c.mu.Unlock()
}

func (c *collector) waitFor(t *testing.T, sub string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		got := c.buf.String()
		c.mu.Unlock()
		if strings.Contains(got, sub) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.mu.Lock()
	got := c.buf.String()
	c.mu.Unlock()
	t.Fatalf("timeout esperando %q; salida=%q", sub, got)
}

// Happy path: la salida del shell llega a onData.
func TestSession_StreamsOutput(t *testing.T) {
	c := &collector{}
	s, err := Start("sh", []string{"-c", "printf READY"}, 80, 24, c.write)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()
	c.waitFor(t, "READY")
}

// El input escrito al pty vuelve por la salida (cat hace echo).
func TestSession_WriteRoundTrips(t *testing.T) {
	c := &collector{}
	s, err := Start("cat", nil, 80, 24, c.write)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()
	if _, err := s.Write([]byte("ping\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	c.waitFor(t, "ping")
}

// Edge: redimensionar una sesion viva no falla.
func TestSession_Resize(t *testing.T) {
	c := &collector{}
	s, err := Start("cat", nil, 80, 24, c.write)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()
	if err := s.Resize(120, 40); err != nil {
		t.Fatalf("Resize: %v", err)
	}
}

// Edge: tras Close el pty queda cerrado y escribir falla (no quedan shells colgados).
func TestSession_CloseStopsShell(t *testing.T) {
	c := &collector{}
	s, err := Start("cat", nil, 80, 24, c.write)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := s.Write([]byte("x")); err == nil {
		t.Fatal("Write tras Close deberia fallar")
	}
}
