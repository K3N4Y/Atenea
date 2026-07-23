import type { ITheme } from '@xterm/xterm'

// Fuente del proyecto: identica a --font-mono de styles/main.css.
export const FONT =
  "'Red Hat Mono', ui-monospace, SFMono-Regular, Menlo, monospace"

// Paper: la identidad clara del proyecto (fondo crema, texto negro calido,
// cursor/seleccion en el acento naranja). Paleta ANSI afinada para fondo claro:
// tonos apagados y 'white' mapeado a grises oscuros para que el texto siga
// legible sobre el papel.
// Para un theme nuevo: copia este objeto, renombralo y cambia los hex.
export const paper: ITheme = {
  background: '#fef9ed', // --color-paper
  foreground: '#1c1c1a', // texto del body
  cursor: '#f97316', // --color-accent
  cursorAccent: '#fef9ed',
  selectionBackground: 'rgba(249, 115, 22, 0.22)', // = ::selection global
  black: '#1c1c1a',
  red: '#c2362f',
  green: '#4f7a3a',
  yellow: '#b8851f',
  blue: '#2f6fb0',
  magenta: '#9b3fb0',
  cyan: '#2a8a8a',
  white: '#6b675f',
  brightBlack: '#8a857c',
  brightRed: '#d6453d',
  brightGreen: '#5e9147',
  brightYellow: '#c89a2b',
  brightBlue: '#3a82c9',
  brightMagenta: '#b24fc9',
  brightCyan: '#34a3a3',
  brightWhite: '#3a3a36',
}

// ponytail: el theme activo es una referencia, no un registry. Cambiar de theme
// = editar esta linea. Selector en runtime (si algun dia hace falta): basta con
// term.options.theme = otroTheme, sin arquitectura previa.
export const active: ITheme = paper
