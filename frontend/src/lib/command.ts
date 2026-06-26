// Slash-commands del composer (logica pura, sin DOM). detectCommand lee el token
// "/..." al inicio del mensaje; filterCommands ordena candidatos; applyCommand
// inserta el comando elegido. ChatComposer los orquesta con el textarea y el menu,
// en paralelo al @-menu de archivos (mention.ts). A diferencia del @, un comando es
// TODO el mensaje: solo dispara cuando "/" es el primer caracter.

// Command es un slash-command tal como lo consume el menu: nombre + descripcion.
// El backend (ListCommands) lo deriva de las skills; el store mapea a esta forma.
export interface Command {
  name: string
  description: string
}

export interface CommandQuery {
  // active = hay un token "/" vigente al inicio que debe abrir el menu.
  active: boolean
  // query = texto entre el "/" y el caret (lo que se usa para filtrar).
  query: string
  // start = indice del "/" (siempre 0 cuando es activo); end = posicion del caret.
  start: number
  end: number
}

const INACTIVE: CommandQuery = { active: false, query: '', start: -1, end: -1 }

// detectCommand reconoce un comando solo cuando "/" es el primer caracter del
// texto y el caret sigue dentro del nombre (sin ningun espacio en blanco entre el
// "/" y el caret). Al teclear el primer espacio el menu se cierra: lo que sigue son
// los argumentos del comando. La query es el texto entre el "/" y el caret.
export function detectCommand(text: string, caret: number): CommandQuery {
  if (caret < 0 || caret > text.length) return INACTIVE
  if (text[0] !== '/') return INACTIVE
  for (let i = 1; i < caret; i++) {
    if (/\s/.test(text[i])) return INACTIVE
  }
  return { active: true, query: text.slice(1, caret), start: 0, end: caret }
}

// filterCommands ordena comandos contra una query (sin distinguir mayusculas).
// Query vacia devuelve la cabeza de la lista. Si no, conserva los comandos cuyo
// nombre (o, en su defecto, descripcion) contiene la query, rankeando el prefijo
// del nombre antes que la subcadena y antes que el match en la descripcion;
// desempata por nombre mas corto y luego alfabetico. Acota a limit.
export function filterCommands(
  commands: Command[],
  query: string,
  limit = 10,
): Command[] {
  if (limit <= 0) return []
  const q = query.toLowerCase()
  if (!q) return commands.slice(0, limit)
  const scored: { cmd: Command; score: number }[] = []
  for (const cmd of commands) {
    const name = cmd.name.toLowerCase()
    let score: number
    if (name.startsWith(q)) score = 0
    else if (name.includes(q)) score = 1
    else if (cmd.description.toLowerCase().includes(q)) score = 2
    else continue
    scored.push({ cmd, score })
  }
  scored.sort(
    (a, b) =>
      a.score - b.score ||
      a.cmd.name.length - b.cmd.name.length ||
      (a.cmd.name < b.cmd.name ? -1 : 1),
  )
  return scored.slice(0, limit).map((s) => s.cmd)
}

// applyCommand reemplaza el token "/..." vigente por "/<name> " (con espacio final)
// y devuelve el texto nuevo y el caret tras el, listo para escribir los argumentos
// del comando o enviarlo con Enter. start es 0, asi que conserva lo que hubiera
// despues del caret.
export function applyCommand(
  text: string,
  m: CommandQuery,
  name: string,
): { text: string; caret: number } {
  const insert = `/${name} `
  const next = insert + text.slice(m.end)
  return { text: next, caret: insert.length }
}
