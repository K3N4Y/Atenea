// Package tool implementa el registry de herramientas: Materialize, settle y los
// builtins. M4 aterrizo el Registry (Materialize/Settle), los tipos del contrato
// (Tool, Call, Result, Permissions, Materialized, UnknownToolError), el
// OutputStore que acota el output grande por callID y el primer builtin
// ejecutable, Echo. Los builtins restantes (bash, read, edit, write, grep, glob)
// llegan despues con sus tests.
package tool
