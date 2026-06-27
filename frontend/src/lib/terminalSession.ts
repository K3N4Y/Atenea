import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { StartPty, ResizePty, ClosePty } from '../../wailsjs/go/main/App'
import { connectTerminal } from './terminal'

// Registro de terminales por id: cada xterm + su pty viven a nivel de modulo, NO
// atados a ningun componente. Asi sobreviven a cerrar el panel o cambiar de tab —
// el shell sigue vivo y el scrollback se conserva. El elemento se "presta" al
// componente montado (attach) y se devuelve detached al desmontar (detach); solo
// destroy (cerrar la tab) mata el pty.
type State = {
  el: HTMLDivElement
  term: Terminal
  fit: FitAddon
  opened: boolean
  started: boolean
}

const reg = new Map<string, State>()

function ensure(id: string): State {
  let st = reg.get(id)
  if (st) return st
  const el = document.createElement('div')
  el.style.width = '100%'
  el.style.height = '100%'
  const term = new Terminal({
    fontSize: 13,
    fontFamily: 'monospace',
    cursorBlink: true,
  })
  const fit = new FitAddon()
  term.loadAddon(fit)
  st = { el, term, fit, opened: false, started: false }
  reg.set(id, st)
  return st
}

// attach mete el elemento de la sesion id en container y, la primera vez, abre el
// xterm y arranca el pty (StartPty + connectTerminal una sola vez). En montajes
// posteriores solo reajusta el tamano: el buffer ya esta ahi.
export async function attach(id: string, container: HTMLElement) {
  const st = ensure(id)
  container.appendChild(st.el)
  if (!st.opened) {
    st.term.open(st.el) // el ya esta en el DOM vivo: xterm puede medir
    st.opened = true
  }
  st.fit.fit()
  if (!st.started) {
    st.started = true
    await StartPty(id, st.term.cols, st.term.rows)
    connectTerminal(id, st.term) // suscripcion de por vida (hasta destroy)
  } else {
    st.term.refresh(0, st.term.rows - 1) // redibuja tras re-adjuntar
    void ResizePty(id, st.term.cols, st.term.rows)
  }
  st.term.focus()
}

// detach saca el elemento del DOM vivo para que el componente desmonte limpio,
// SIN cerrar el pty: el shell sigue corriendo y el buffer queda intacto.
export function detach(id: string) {
  reg.get(id)?.el.remove()
}

// resize reajusta el pty al tamano del contenedor (ResizeObserver del componente).
export function resize(id: string) {
  const st = reg.get(id)
  if (!st) return
  st.fit.fit()
  void ResizePty(id, st.term.cols, st.term.rows)
}

// destroy mata el pty y libera el xterm: se llama al CERRAR la tab (no al cambiar
// de tab ni cerrar el panel, que solo desmontan).
export function destroy(id: string) {
  const st = reg.get(id)
  if (!st) return
  reg.delete(id)
  st.el.remove()
  void ClosePty(id)
  st.term.dispose()
}
