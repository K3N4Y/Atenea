package subagent

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"
)

// RunBatchClient is the transport-only side of script spawning. It connects to
// the already-running harness and never loads a provider, credentials, or a
// second application host.
func RunBatchClient(stdin io.Reader, stdout, stderr io.Writer) int {
	socket, token := os.Getenv(BatchSocketEnv), os.Getenv(BatchTokenEnv)
	if socket == "" || token == "" {
		fmt.Fprintln(stderr, "atenea: RAH batch client is unavailable outside a harness bash call")
		return 5
	}
	dec := json.NewDecoder(io.LimitReader(stdin, maxBatchRequestBytes+1))
	dec.DisallowUnknownFields()
	var request BatchRequest
	if err := dec.Decode(&request); err != nil {
		fmt.Fprintln(stderr, "atenea: invalid RAH batch request:", err)
		return 2
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		fmt.Fprintln(stderr, "atenea: invalid trailing RAH batch data")
		return 2
	}
	if request.Token != "" {
		fmt.Fprintln(stderr, "atenea: RAH token must not be supplied in request JSON")
		return 2
	}
	request.Token = token
	conn, err := net.DialTimeout("unix", socket, 5*time.Second)
	if err != nil {
		fmt.Fprintln(stderr, "atenea: connect to RAH harness:", err)
		return 5
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		fmt.Fprintln(stderr, "atenea: send RAH batch:", err)
		return 5
	}
	if unix, ok := conn.(*net.UnixConn); ok {
		_ = unix.CloseWrite()
	}
	var response BatchResponse
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		fmt.Fprintln(stderr, "atenea: receive RAH batch:", err)
		return 5
	}
	if err := json.NewEncoder(stdout).Encode(response); err != nil {
		fmt.Fprintln(stderr, "atenea: write RAH response:", err)
		return 5
	}
	for _, result := range response.Results {
		if strings.ToLower(result.Status) != "ok" {
			return 1
		}
	}
	return 0
}
