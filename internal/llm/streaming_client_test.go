package llm

import (
	"net/http"
	"testing"
	"time"
)

func TestStreamingHTTPClientEnablesHTTP2Keepalive(t *testing.T) {
	client := streamingHTTPClient(time.Second)
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("client.Transport = %T, want *http.Transport", client.Transport)
	}
	if transport.HTTP2 == nil || transport.HTTP2.SendPingTimeout <= 0 {
		t.Fatal("streaming transport has no HTTP/2 ping health check; a dead connection would hang the turn until the peer resets it")
	}
	if transport.ResponseHeaderTimeout != time.Second {
		t.Errorf("ResponseHeaderTimeout = %v, want %v", transport.ResponseHeaderTimeout, time.Second)
	}
}
