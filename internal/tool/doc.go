// Package tool implementa el registry de herramientas: Materialize, settle y los
// builtins. M4 aterrizo el Registry (Materialize/Settle), los tipos del contrato
// (Tool, Call, Result, Permissions, Materialized, UnknownToolError), el
// OutputStore que acota el output grande por callID y el primer builtin
// ejecutable, Echo. Ademas aterrizaron read y edit, apoyados en el motor hashline
// del subpaquete internal/tool/hashline (hash de frescura + snapshot del archivo
// + lineas vistas): el read numera lineas tras un header [path#HASH] y graba el
// snapshot; el edit aplica un patch hashline anclado contra ese header y consume
// el SnapshotStore que el read grabo (comparten root y snaps a nivel app). Los
// builtins restantes (bash, write, grep, glob) siguen pendientes con sus tests.
package tool
