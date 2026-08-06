package llm

import (
	"errors"
	"fmt"
	"io"
	"syscall"
	"testing"
)

func TestIsTransientStreamError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil is not transient", err: nil, want: false},
		{
			name: "http2 rst_stream from peer",
			err:  errors.New("stream error: stream ID 61; INTERNAL_ERROR; received from peer"),
			want: true,
		},
		{
			name: "rst_stream wrapped with provider label",
			err:  fmt.Errorf("PostHog (gpt-5.6-luna): %w", errors.New("stream error: stream ID 7; INTERNAL_ERROR; received from peer")),
			want: true,
		},
		{
			name: "server goaway",
			err:  errors.New("http2: server sent GOAWAY and closed the connection; LastStreamID=5, ErrCode=NO_ERROR"),
			want: true,
		},
		{
			name: "keepalive ping unanswered",
			err:  errors.New("http2: client connection lost"),
			want: true,
		},
		{
			name: "typed connection reset",
			err:  fmt.Errorf("read tcp 1.2.3.4:443: %w", syscall.ECONNRESET),
			want: true,
		},
		{
			name: "body cut mid-stream",
			err:  fmt.Errorf("decoding event: %w", io.ErrUnexpectedEOF),
			want: true,
		},
		{
			name: "context overflow is acted on by compaction, not retry",
			err:  &ContextOverflowError{Message: "prompt is too long"},
			want: false,
		},
		{
			name: "api rejection reproduces on retry",
			err:  errors.New("invalid_request_error: model not found"),
			want: false,
		},
		{
			name: "auth failure reproduces on retry",
			err:  errors.New("the credential is no longer accepted; log in again"),
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsTransientStreamError(tc.err); got != tc.want {
				t.Errorf("IsTransientStreamError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
