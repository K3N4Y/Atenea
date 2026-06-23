// Package tool implementa el registry de herramientas: Materialize, settle y los
// builtins. M4 aterrizo el Registry (Materialize/Settle), los tipos del contrato
// (Tool, Call, Result, Permissions, Materialized, UnknownToolError), el
// OutputStore que acota el output grande por callID y el primer builtin
// ejecutable, Echo. Ademas aterrizaron read y edit, apoyados en el motor hashline
// del subpaquete internal/tool/hashline (hash de frescura + snapshot del archivo
// + lineas vistas): el read numera lineas tras un header [path#HASH] y graba el
// snapshot; el edit aplica un patch hashline anclado contra ese header y consume
// el SnapshotStore que el read grabo; el write crea un archivo nuevo con su
// contenido completo (la via para archivos nuevos, que el edit no puede crear) y
// graba su snapshot para que un edit posterior ancle sin re-leer (los tres
// comparten root y snaps por sesion). glob busca archivos por patron ripgrep y
// devuelve rutas relativas al workspace; grep busca contenido y devuelve lineas en
// formato hashline para encadenar edit. bash ya aterrizo: corre comandos con
// bash -c por llamada (sin sesion persistente, el cwd y el env no sobreviven entre
// calls), combina stdout+stderr, aplica un timeout por tiers (rapido por defecto,
// lento con slow_ok), mata el grupo de procesos al expirar, scrubea los secretos
// del env y acota el output head+tail.
package tool
