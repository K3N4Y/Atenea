//go:build !unix

package tool

import "os/exec"

// setBashProcessGroup no hace nada fuera de unix: no hay grupos de procesos
// portables, asi que solo se mata el proceso directo.
func setBashProcessGroup(cmd *exec.Cmd) {}

// killBashProcessGroup mata solo el proceso directo: el fallback sin grupos.
func killBashProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
