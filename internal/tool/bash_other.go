//go:build !unix

package tool

import "os/exec"

// setProcessGroup no hace nada fuera de unix: no hay grupos de procesos
// portables, asi que solo se mata el proceso directo.
func setProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup mata solo el proceso directo: el fallback sin grupos.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
