package llm

import (
	"errors"
	"io"
	"strings"
	"syscall"
)

// transientStreamMarkers name the transport interruptions a retry can outlive,
// as they print. The SDKs do not always keep the typed cause when a stream
// dies mid-body, and net/http's bundled HTTP/2 errors are internal types, so
// the rendered form is the only stable classifier for most of them.
var transientStreamMarkers = []string{
	"stream error:",                 // HTTP/2 RST_STREAM, e.g. "stream error: stream ID 61; INTERNAL_ERROR; received from peer"
	"http2: server sent goaway",     // the peer is shedding the connection
	"http2: client connection lost", // a keepalive ping went unanswered
	"connection reset by peer",
	"broken pipe",
	"unexpected eof",
	"server closed idle connection",
}

// IsTransientStreamError reports whether a stream failure is a transport
// interruption worth retrying with the same request: the peer reset the
// stream or connection, or the connection died mid-body. API-level failures
// (auth, invalid request, context overflow) are not transient — retrying them
// reproduces the same failure.
func IsTransientStreamError(err error) bool {
	if err == nil {
		return false
	}
	var overflow *ContextOverflowError
	if errors.As(err, &overflow) {
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, marker := range transientStreamMarkers {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
