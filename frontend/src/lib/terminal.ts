import { WritePty, ClosePty } from '../../wailsjs/go/main/App'
import { EventsOn } from '../../wailsjs/runtime/runtime'

// Canal de la salida del pty, uno por sesion (coincide con ptyChannel en el backend).
const ptyData = (id: string) => `pty:data:${id}`

// TermLike es el minimo que necesitamos de xterm.js. Tenerlo como contrato hace
// connectTerminal testeable con un term falso, sin montar un xterm real (que no
// corre headless).
export type TermLike = {
  write(data: Uint8Array): void
  onData(cb: (data: string) => void): void
}

// b64ToBytes decodifica el base64 con el que Wails serializa el []byte del pty.
// Se pasa a term.write como bytes (no string) para no corromper UTF-8 partido
// entre chunks: xterm reensambla los bytes.
export function b64ToBytes(b64: string): Uint8Array {
  const bin = atob(b64)
  const out = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
  return out
}

// connectTerminal puentea el pty `id` (ya arrancado) con un term tipo xterm:
// salida del backend -> term.write, input del term -> WritePty. Devuelve un
// dispose que desuscribe y cierra el pty. El caller arranca el pty (StartPty)
// cuando ya sabe el tamano; aca solo se cablea la E/S.
export function connectTerminal(id: string, term: TermLike): () => void {
  const off = EventsOn(ptyData(id), (b64: string) => term.write(b64ToBytes(b64)))
  term.onData((d) => WritePty(id, d))
  return () => {
    off()
    ClosePty(id)
  }
}
