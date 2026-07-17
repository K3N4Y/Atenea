package main

import (
	"context"
	"errors"
	"os"
	"runtime"

	"atenea/internal/terminal"
)

// shutdown (OnShutdown de Wails) mata todos los shells vivos al cerrar la app,
// para no dejar procesos colgados.
func (a *App) shutdown(_ context.Context) {
	a.term.CloseAll()
	a.mcp.Close()
}

// ptyChannel es el canal por el que una tab Terminal recibe la salida de SU shell
// (uno por id, para que cada terminal sea independiente). La frontera (a.emit) es
// la misma de event.Bus.
func ptyChannel(id string) string { return "pty:data:" + id }

// shellPath elige el shell interactivo: $SHELL si esta seteado, si no /bin/bash y
// por ultimo /bin/sh. ponytail: solo Unix por ahora; en Windows seria cmd/pwsh.
func shellPath() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	if runtime.GOOS == "windows" {
		return "powershell.exe"
	}
	if _, err := os.Stat("/bin/bash"); err == nil {
		return "/bin/bash"
	}
	return "/bin/sh"
}

// StartPty arranca un shell bajo un pty para la tab Terminal con ese id y emite su
// salida (bytes; Wails los serializa en base64) al canal pty:data:<id>. Re-arrancar
// el mismo id reemplaza la sesion.
func (a *App) StartPty(id string, cols, rows uint16) error {
	return a.term.Start(id, shellPath(), nil, cols, rows, func(b []byte) {
		if a.emit != nil {
			a.emit(ptyChannel(id), b)
		}
	})
}

// WritePty manda al pty de id lo que el usuario teclea. Tolera ErrNoSession (carrera
// de teclas antes de que StartPty registre la sesion): no es un error a propagar.
func (a *App) WritePty(id, data string) error {
	if err := a.term.Write(id, []byte(data)); err != nil && !errors.Is(err, terminal.ErrNoSession) {
		return err
	}
	return nil
}

// ResizePty propaga al pty de id el nuevo tamano que calcula el FitAddon de xterm.
func (a *App) ResizePty(id string, cols, rows uint16) error {
	if err := a.term.Resize(id, cols, rows); err != nil && !errors.Is(err, terminal.ErrNoSession) {
		return err
	}
	return nil
}

// ClosePty mata el shell de id al cerrar su tab. Idempotente.
func (a *App) ClosePty(id string) error { return a.term.Close(id) }
