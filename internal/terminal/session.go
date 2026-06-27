// Package terminal arranca un shell bajo un pseudo-terminal (pty) y bombea su
// salida a un callback. Es el nucleo testeable de la tab Terminal del panel de
// desarrollo: no conoce Wails ni el nombre del canal de eventos; quien lo usa
// (App) cablea onData a la frontera de emit.
package terminal

import (
	"os"
	"os/exec"

	"github.com/creack/pty"
)

// Session es un shell vivo corriendo bajo un pty. Su salida fluye a onData hasta
// que el shell termina o se llama a Close.
type Session struct {
	f   *os.File
	cmd *exec.Cmd
}

// Start lanza name (con args) bajo un pty de cols x rows y manda su salida a
// onData en chunks. La lectura corre en una goroutine; onData se invoca ahi.
func Start(name string, args []string, cols, rows uint16, onData func([]byte)) (*Session, error) {
	cmd := exec.Command(name, args...)
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return nil, err
	}
	s := &Session{f: f, cmd: cmd}
	go func() {
		// ponytail: buffer fijo de 4KB; el read del pty ya entrega lo que haya.
		buf := make([]byte, 4096)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				onData(chunk)
			}
			if err != nil {
				return // EOF o pty cerrado: el shell murio.
			}
		}
	}()
	return s, nil
}

// Write manda input crudo al pty (lo que el usuario teclea en xterm).
func (s *Session) Write(p []byte) (int, error) { return s.f.Write(p) }

// Resize ajusta el tamano del pty cuando xterm cambia de dimensiones.
func (s *Session) Resize(cols, rows uint16) error {
	return pty.Setsize(s.f, &pty.Winsize{Cols: cols, Rows: rows})
}

// Close mata el shell y cierra el pty, para no dejar procesos colgados al cerrar
// la tab. El Kill es best-effort: si el shell ya murio, basta cerrar el pty.
func (s *Session) Close() error {
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	return s.f.Close()
}
