// Package subagent provee la tool "task": delega una tarea a un subagente hijo.
//
// La tool levanta un runner hijo (un runner con MemoryStore en memoria) para
// ejecutar la def del subagente y devolver su reporte final. Vive en
// internal/session/subagent y no en internal/tool porque necesita el runner
// (internal/session/runner, que importa internal/tool); ponerla en internal/tool
// seria un ciclo. Execute parsea {subagent_type, prompt}, busca la def, levanta
// el hijo y devuelve su reporte final (el ultimo texto del asistente del hijo).
//
// La profundidad de anidamiento se propaga por el context y al alcanzar el tope
// maxDepth el subagente hijo deja de recibir la tool "task", evitando recursion
// infinita.
//
// La tool tambien topa cuantos subagentes corren a la vez con un cap de
// concurrencia configurable (estilo worker-pool): cuando el padre lanza muchos
// "task" en paralelo los excedentes esperan un slot libre, para no desbordar
// recursos con una avalancha de runners hijos simultaneos.
package subagent
