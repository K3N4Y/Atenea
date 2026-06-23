//go:build unix

package tool

import (
	"os/exec"
	"syscall"
)

// setBashProcessGroup pone al comando en su propio grupo de procesos para poder
// matar al arbol entero (hijo y nietos) al cancelar.
func setBashProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killBashProcessGroup mata el grupo de procesos completo enviando SIGKILL al
// PID negativo (el grupo). Sin esto un "sleep 5 &" dejaria huerfanos.
func killBashProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
