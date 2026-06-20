package runner

import (
	"testing"

	"golang.org/x/sync/errgroup"
)

// TestRunnerPackageWiresErrgroup fija el nombre del paquete y deja cableada la
// dependencia errgroup que el loop de M5 usara para asentar tools concurrentes.
// El import real ancla errgroup en go.mod para que `go mod tidy` no la quite.
func TestRunnerPackageWiresErrgroup(t *testing.T) {
	var _ errgroup.Group
}
