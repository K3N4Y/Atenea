# Spec - Tool `glob` (busqueda de archivos estilo opencode)

Spec ejecutable de la **tool `glob`**, basada en la implementacion real de
opencode (`packages/core/src/tool/glob.ts`, `filesystem.ts`, `ripgrep.ts` y
`filesystem/schema.ts`, investigadas el 2026-06-22). No es un hito numerado del
loop (`docs/atenea-agent-loop-roadmap.md` cierra en M10): es la siguiente tool de
navegacion de codigo despues de `read`/`write`/`edit`.

`glob` no lee contenido y no modifica archivos. Su trabajo es encontrar archivos
por patron dentro del workspace para que el modelo pueda elegir rutas concretas y
luego usar `read`. Se trabaja con el ciclo de `AGENTS.md`
(`Safety net -> Understand -> RED -> GREEN -> TRIANGULATE -> REFACTOR -> Evidence`).
Texto en espanol sin acentos.

## 1. Contexto

Atenea ya tiene:

- `internal/tool/registry.go`: contrato de tools (`Tool`, `Result`, `Settle`,
  `Permissions`) y `OutputStore`.
- `internal/tool/read.go`: lee archivos bajo `Root`, usa rutas relativas al
  workspace y rechaza escapes de sandbox.
- `internal/tool/write.go` y `internal/tool/edit.go`: comparten el mismo contrato
  de ruta relativa y la misma compuerta `sandboxJoin`.
- `app.go`: registra `echo`, `read`, `write` y `edit`; `glob` sigue pendiente en
  `internal/tool/doc.go`.

La referencia de produccion en opencode hace tres cosas importantes:

1. La tool expone `pattern`, `path` opcional y `limit` opcional.
2. Ejecuta la busqueda con ripgrep usando `rg --files --glob=<pattern>` y excluye
   `.git` con `--glob=!**/.git/**`.
3. Convierte el resultado a texto line-oriented: una ruta por linea, o `No files
   found` si no hay resultados.

Hay una divergencia deliberada para Atenea: opencode acaba mostrando rutas
absolutas al modelo en `toModelOutput`; Atenea debe devolver rutas **relativas al
workspace**, porque `read`, `write` y `edit` aceptan rutas relativas y rechazan
absolutas. Si `glob` devolviera absolutas, empujaria al modelo a llamar `read` con
un path que Atenea va a rechazar.

## 2. Objetivo

Dejar especificada la tool `glob` para encontrar archivos bajo `Root` con un
patron de glob ripgrep, con salida compacta y segura:

En `internal/tool`:

- `glob.go`: `GlobTool`, `NewGlobTool`, `GlobSearcher`, `RipgrepGlobSearcher`,
  tipos internos `GlobSearch`, `GlobEntry`, `GlobSearchResult`, validacion de
  input y formateo del output.
- `glob_test.go`: tests de la tool contra un searcher fake y del adapter ripgrep
  contra un runner fake.
- `doc.go`: actualizar la lista de builtins para que `glob` deje de figurar como
  pendiente.

En `app.go`:

- construir `tool.NewGlobTool(root)` con la misma raiz que `read/write/edit`,
  registrarlo en `NewRegistry(...)` y permitir `"glob": true`.

Esta fase **no** implementa `grep`, `bash`, busqueda de contenido, busqueda fuzzy,
listado de directorios, inclusion de archivos ocultos, seguimiento de symlinks ni
permisos ricos por patron de ruta.

## 3. Alcance

### Dentro

- Input JSON:
  - `pattern` (string, requerido): patron glob de ripgrep.
  - `path` (string, opcional): directorio relativo desde donde buscar; default
    `"."`.
  - `limit` (int positivo, opcional): maximo de resultados a devolver.
- Resolucion de `path` bajo `Root` con `sandboxJoin`.
- En FS real, rechazo de symlink/realpath fuera de `Root` con
  `rejectRealPathOutside`.
- Ejecucion con ripgrep:
  - `rg --no-config --files --glob=<pattern> --glob=!**/.git/** .`
  - cwd = `Root/path`.
  - sin `--hidden` y sin `--follow` en v1.
- Normalizacion de filas:
  - quitar prefijos `./`, `/` y `\`.
  - convertir `\` a `/`.
  - convertir cada resultado relativo al cwd en ruta relativa al workspace.
  - fallar cerrado si alguna fila resuelta escapa de `Root`.
- Output:
  - cero resultados -> `No files found`.
  - resultados -> una ruta relativa por linea.
  - si hay mas resultados que `limit` -> anexar notice in-band.
- Tests con fake searcher/runner, sin depender de tener `rg` instalado.
- Wiring en `app.go` y permiso `"glob": true`.

### Fuera

- `grep`/busqueda de contenido por regex.
- `find` fuzzy por nombre.
- Listado de directorios como tool separada.
- Soporte para archivos ocultos (`--hidden`) o seguir symlinks (`--follow`).
- Patrones de include/exclude multiples.
- Tipos MIME en el output del modelo. Opencode usa `FileSystem.Entry{path,type,mime}`;
  Atenea v1 solo necesita rutas porque el contrato `Tool.Result` es texto.
- Permisos ricos (`ask`, allow por patron, auditoria de recursos). Sigue el set de
  nombres de M4: `"glob": true`.
- Dependencia embebida de ripgrep. v1 usa el binario `rg` disponible en el sistema;
  si no existe, devuelve error accionable.

## 4. Contrato visible de la tool

### 4.1 Nombre y descripcion

```go
func (*GlobTool) Name() string { return "glob" }

func (*GlobTool) Description() string {
	return "Encuentra archivos por patron glob dentro del workspace. Devuelve rutas relativas, una por linea; usa path para acotar el directorio y limit para acotar resultados."
}
```

### 4.2 Schema

El schema debe mantener la forma de opencode (`pattern`, `path`, `limit`) y ser
compatible con el `llm.ToolDef` actual:

```json
{
  "type": "object",
  "properties": {
    "pattern": {
      "type": "string",
      "description": "Patron glob para encontrar archivos, con semantica de ripgrep (por ejemplo \"*.go\", \"**/*.go\" o \"internal/**/*.go\")."
    },
    "path": {
      "type": "string",
      "description": "Directorio relativo al workspace donde buscar. Default: \".\"."
    },
    "limit": {
      "type": "integer",
      "minimum": 1,
      "description": "Maximo de resultados a devolver."
    }
  },
  "required": ["pattern"]
}
```

### 4.3 Output

Sin resultados:

```text
No files found
```

Con resultados:

```text
app.go
internal/tool/read.go
internal/tool/edit.go
```

Con `path: "internal"` el output sigue siendo relativo al workspace, no relativo
al cwd de la busqueda:

```text
internal/tool/read.go
internal/tool/edit.go
```

Si se excede el limite:

```text
internal/tool/edit.go
internal/tool/read.go

[Limit reached: showing first 2 files. Use a narrower pattern or higher limit.]
```

El notice de limite es una mejora de Atenea sobre el leaf de opencode: el adapter
de opencode detecta truncado al leer `limit + 1`, pero `glob.ts` descarta ese flag
al convertir a texto. Atenea conserva el aviso porque el modelo solo ve el string
del `Tool.Result` y el `OutputStore` recorta por bytes sin semantica de busqueda.

## 5. Tipos y contrato interno

### 5.1 `internal/tool/glob.go`

```go
type GlobTool struct {
	Root          string
	Searcher      GlobSearcher
	DefaultLimit  int
	MaxLimit      int
}

type GlobSearch struct {
	Cwd     string // ruta absoluta desde la que se ejecuta la busqueda
	Pattern string
	Limit   int
}

type GlobEntry struct {
	Path string // relativo a Cwd, normalizado con slash
}

type GlobSearchResult struct {
	Entries   []GlobEntry
	Truncated bool
}

type GlobSearcher interface {
	Glob(ctx context.Context, input GlobSearch) (GlobSearchResult, error)
}
```

Constantes sugeridas:

```go
const (
	defaultGlobLimit = 200
	maxGlobLimit     = 5000
)
```

`DefaultLimit` evita que `pattern: "**/*"` enumere un repositorio entero cuando el
modelo omite `limit`. `MaxLimit` evita que un input mal elegido fuerce un output
gigante; el `OutputStore` sigue siendo la ultima defensa por bytes.

### 5.2 `RipgrepGlobSearcher`

```go
type RipgrepGlobSearcher struct {
	Binary string // default "rg"
	Runner lineRunner
}

type lineRunner interface {
	RunLines(ctx context.Context, cwd, binary string, args []string, limit int) (lines []string, truncated bool, err error)
}
```

El runner real usa `os/exec` con `exec.CommandContext`. Debe leer stdout por lineas
y parar tras `limit + 1` filas para saber si hubo truncado sin cargar todo el
output en memoria. `stderr` se acota (por ejemplo 8 KiB) para errores
accionables, igual que opencode acota `ERROR_BYTES`.

Args de produccion:

```text
--no-config
--files
--glob=<pattern>
--glob=!**/.git/**
.
```

Decisiones:

- No pasar `--hidden` en v1: igual que la tool `glob` de opencode, que solo tiene
  `pattern/path/limit` y no expone `hidden`.
- No pasar `--follow` en v1: evita seguir symlinks fuera del workspace.
- Codigo de salida `0`: resultados normales.
- Codigo de salida `1`: sin resultados -> lista vacia.
- Codigo de salida `2` o superior: error de tool con stderr acotado. Opencode
  permite algunos parciales con codigo 2; Atenea v1 falla cerrado para no mostrar
  resultados incompletos como si fueran completos.

### 5.3 Flujo de `Execute`

1. `json.Unmarshal` del input.
2. Validar:
   - `pattern` no vacio.
   - `limit` ausente -> `DefaultLimit`.
   - `limit <= 0` -> error de tool.
   - `limit > MaxLimit` -> error de tool con mensaje que nombre el maximo.
   - `path` ausente o vacio -> `"."`.
3. Resolver `path` con `sandboxJoin(gt.Root, path, "glob")`.
4. Si `Searcher` es el real (`RipgrepGlobSearcher`), correr
   `rejectRealPathOutside(gt.Root, cwd, path, "glob")` antes de buscar. Si el path
   no existe, el searcher devolvera error accionable.
5. Llamar `Searcher.Glob(ctx, GlobSearch{Cwd: cwd, Pattern: pattern, Limit: limit})`.
6. Convertir cada `GlobEntry.Path` (relativo a `cwd`) a ruta relativa a `Root`:
   - limpiar prefijos `./`, `/`, `\`.
   - `filepath.Join(cwd, entry.Path)`.
   - comprobar `insideRoot(rootAbs, absEntry)`.
   - `filepath.Rel(rootAbs, absEntry)`.
   - `filepath.ToSlash`.
7. Si cualquier fila escapa de `Root`, devolver error y no emitir output parcial.
8. Formatear:
   - sin entradas -> `No files found`.
   - entradas -> join con `\n`.
   - si `Truncated` -> linea vacia + notice de limite.
9. Devolver `tool.Result{Output: text}`. `Result.Truncated` lo decide el
   `OutputStore`, no la tool.

## 6. Plan TDD

Se ataca de afuera hacia adentro: primero el comportamiento que ve el modelo
(`GlobTool` con fake searcher), luego el adapter `RipgrepGlobSearcher` con runner
fake, y al final wiring.

### Safety net

- Estado base antes de tocar codigo: `go test ./...`, `go vet ./...`,
  `gofmt -l .`.
- Si falla antes de editar, reportar como preexistente y no seguir a ciegas.

### Understand

- Leer este spec.
- Leer `internal/tool/registry.go`, `output.go`, `path.go`, `read.go`, `write.go`,
  `edit.go`, `doc.go` y `app.go`.
- Leer referencias de opencode:
  - `packages/core/src/tool/glob.ts`
  - `packages/core/src/filesystem.ts`
  - `packages/core/src/ripgrep.ts`
  - `packages/core/src/filesystem/schema.ts`
- Comportamiento esperado: busqueda por `rg --files --glob`, path opcional como
  cwd relativo, limite positivo, `.git` excluido, output compacto de paths
  relativos a workspace.

### RED

Escribir estos tests primero. Deben fallar porque `GlobTool`/`GlobSearcher` aun no
existen o porque el comportamiento todavia no esta implementado.

1. `TestGlobTool_FindsFilesByPattern`: fake searcher devuelve
   `{"app.go","internal/tool/read.go"}` -> output con esas dos rutas, una por
   linea.
2. `TestGlobTool_NoFilesFound`: fake searcher devuelve cero entradas -> `No files
   found`.
3. `TestGlobTool_PathNarrowsSearchAndOutputsWorkspaceRelativePaths`: input
   `{pattern:"*.go", path:"internal"}` hace que el fake reciba `Cwd == "/work/internal"`
   y una entrada `tool/read.go` se emita como `internal/tool/read.go`.
4. `TestGlobTool_LimitBoundsOutputAndShowsNotice`: input `limit:2`, fake devuelve
   `Truncated:true` con dos entradas -> output de dos rutas + notice de limite.
5. `TestRipgrepGlobSearcher_UsesProductionRipgrepArgs`: runner fake verifica args
   `--no-config --files --glob=*.go --glob=!**/.git/** .`.

Comandos:

```bash
go test -run TestGlobTool ./internal/tool
go test -run TestRipgrepGlobSearcher ./internal/tool
```

Resultado esperado en RED: no compila (`undefined: GlobTool`) o falla el assert de
comportamiento.

### GREEN

- Agregar `internal/tool/glob.go` con:
  - `GlobTool`.
  - `NewGlobTool(root string) *GlobTool`.
  - `GlobSearcher` y tipos internos.
  - `formatGlobOutput`.
  - implementacion minima de `Execute` con fake searcher.
- Agregar `RipgrepGlobSearcher` con runner inyectable.
- Correr solo los tests rojos hasta que pasen:

```bash
go test -run TestGlobTool ./internal/tool
go test -run TestRipgrepGlobSearcher ./internal/tool
```

### TRIANGULATE

Agregar casos que evitan falso verde:

- `TestGlobTool_DefaultLimit`: sin `limit`, el fake recibe `defaultGlobLimit`.
- `TestGlobTool_RejectsInvalidLimit`: `limit:0` y `limit:-1` -> error de tool y el
  fake no se llama.
- `TestGlobTool_RejectsLimitAboveMax`: `limit:maxGlobLimit+1` -> error de tool y
  el fake no se llama.
- `TestGlobTool_RejectsEmptyPattern`: `pattern:""` -> error de tool.
- `TestGlobTool_InvalidInputErrors`: input JSON malformado -> error de tool.
- `TestGlobTool_RejectsPathOutsideRoot`: `path:"../secret"` -> error antes de
  llamar al fake.
- `TestGlobTool_RejectsSearcherRowsOutsideRoot`: fake devuelve `../secret.txt` ->
  error fail-closed, sin output parcial.
- `TestGlobTool_NormalizesWindowsSeparators`: fake devuelve `tool\\read.go` bajo
  `path:"internal"` -> output `internal/tool/read.go`.
- `TestGlobTool_SearchErrorBecomesToolError`: fake devuelve error -> `Execute`
  devuelve error accionable que incluye `glob`.
- `TestGlobTool_RejectsSymlinkSearchRootOutsideWorkspace`: con FS real, `path`
  apunta a un symlink dentro de `Root` que resuelve fuera -> error antes de
  ejecutar ripgrep.
- `TestRipgrepGlobSearcher_EmptyExitCodeOneIsNoFiles`: runner fake simula codigo 1
  -> lista vacia, sin error.
- `TestRipgrepGlobSearcher_NonzeroFailureIncludesBoundedStderr`: runner fake simula
  error -> error accionable, stderr acotado.
- `TestRipgrepGlobSearcher_StripsDotSlashAndBackslashes`: stdout `./a\\b.go` ->
  `GlobEntry{Path:"a/b.go"}`.
- `TestGlobTool_ContextCancellationPropagates`: fake observa `ctx.Done()` y
  devuelve `context.Canceled`; `Execute` propaga el error.

Comando:

```bash
go test -run 'TestGlobTool|TestRipgrepGlobSearcher' ./internal/tool
```

### REFACTOR + wiring

- Extraer helpers de test:
  - `globInput(t, pattern, path string, limit *int) json.RawMessage`.
  - `fakeGlobSearcher` que registra `GlobSearch`.
  - `fakeLineRunner` que registra cwd/binary/args.
- Mantener `glob.go` simple. Si el adapter ripgrep crece demasiado, moverlo a
  `ripgrep_glob.go`, pero no antes de necesitarlo.
- Actualizar `internal/tool/doc.go`: `glob` ya no pendiente.
- Actualizar `app.go`:
  - `tool.NewGlobTool(root)` en `NewRegistry`.
  - permiso `"glob": true`.
- Gates de cierre:

```bash
gofmt -l .
go vet ./...
go test ./...
```

## 7. Criterios de aceptacion (Done when)

1. Existe `tool.GlobTool` e implementa `Tool`.
2. `Schema()` anuncia `pattern` requerido, `path` opcional y `limit` opcional
   positivo.
3. `Execute` valida JSON, `pattern`, `limit` y `path`; errores son accionables y
   no llaman al searcher cuando el input es invalido.
4. `path` se resuelve dentro de `Root` y rechaza `..`/absolutas/symlinks fuera de
   workspace antes de buscar en FS real.
5. `RipgrepGlobSearcher` usa los args de opencode: `--no-config --files
   --glob=<pattern> --glob=!**/.git/** .`.
6. No se pasa `--hidden` ni `--follow` en v1.
7. La salida de la tool es `No files found` o rutas relativas al workspace, una por
   linea, normalizadas con `/`.
8. `path` acota la busqueda pero no cambia la base del output: con `path:"internal"`
   se emite `internal/...`.
9. `limit` acota resultados; si hay mas resultados, se agrega notice in-band.
10. Una fila que escaparia de `Root` se rechaza fail-closed, sin output parcial.
11. `glob` esta registrado en `app.go` con `"glob": true` y aparece en las
    `Definitions` materializadas.
12. `go test ./...` pasa; `go vet ./...` limpio; `gofmt -l .` vacio.

## 8. Comandos

```bash
# Safety net
go test ./...
go vet ./...
gofmt -l .

# Ciclo especifico
go test -run TestGlobTool ./internal/tool
go test -run TestRipgrepGlobSearcher ./internal/tool
go test -run 'TestGlobTool|TestRipgrepGlobSearcher' ./internal/tool

# Gates de cierre
gofmt -l .
go vet ./...
go test ./...
```

Si se quiere hacer una prueba manual con el binario real:

```bash
rg --no-config --files --glob='*.go' --glob='!**/.git/**' .
```

## 9. Tabla de evidencia esperada

Al cerrar la fase, la respuesta/PR debe incluir esta tabla con resultados reales:

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Suite verde antes de editar | `go test ./...`, `go vet ./...`, `gofmt -l .` | pass |
| Understand | Specs y referencias opencode leidas | `glob.ts`, `ripgrep.ts`, `filesystem.ts`, `internal/tool/{registry,path,read,write,edit}.go` | comportamiento identificado |
| RED | Tests de tool y adapter escritos primero | `internal/tool/glob_test.go` + `go test -run TestGlobTool ./internal/tool` | fallo esperado |
| GREEN | `GlobTool` + `RipgrepGlobSearcher` minimos | `internal/tool/glob.go` + tests especificos | tests especificos pasan |
| TRIANGULATE | Limites, sandbox, path relativo, normalizacion, errores, truncado, cancelacion | `go test -run 'TestGlobTool|TestRipgrepGlobSearcher' ./internal/tool` | casos pasan |
| REFACTOR | Helpers de test, doc.go y wiring en app.go | `gofmt -l .`, `go vet ./...`, `go test ./...` | suite verde, `glob` registrado |

## 10. Riesgos y decisiones

- **Ripgrep como referencia de produccion.** Se copia el comportamiento central de
  opencode: `rg --files` + `--glob=<pattern>` + exclusion de `.git`. Esto evita
  reimplementar mal `**`, braces, ignore files y detalles de matching.
- **Dependencia externa en `rg`.** v1 asume que `rg` esta instalado. Si no esta,
  error accionable. Empaquetar un binario o hacer fallback puro en Go queda fuera;
  no se paga ese costo antes de necesitarlo.
- **Paths relativos en Atenea.** Opencode termina mostrando absolutos al modelo,
  pero Atenea devuelve relativos porque `read/edit/write` estan diseniadas sobre
  rutas relativas al workspace. Es una divergencia necesaria para que las tools
  compongan.
- **Default limit.** Opencode usa `Number.MAX_SAFE_INTEGER` cuando `limit` falta.
  Atenea usa `defaultGlobLimit` para no enumerar un repo entero accidentalmente.
  El modelo puede subir `limit` si necesita mas, hasta `maxGlobLimit`.
- **Notice de truncado.** Opencode detecta truncado en su adapter generico pero el
  leaf `glob.ts` no lo expone. Atenea lo expone in-band porque el output textual es
  lo unico que ve el modelo.
- **Sin hidden/follow en v1.** Menos superficie y menos riesgo de symlink escape.
  Si despues se necesita, agregar `hidden`/`follow` como parametros explicitos con
  tests de sandbox.
- **Codigo 2 de ripgrep.** Opencode puede conservar parciales en algunos codigo 2.
  Atenea v1 falla cerrado: mejor pedir un patron valido que entregar una lista
  potencialmente incompleta sin contexto.
- **No usar `filepath.Glob`.** No soporta el mismo lenguaje que ripgrep y seria una
  fuente de diferencias con produccion. Si se requiere fallback Go, debe usar una
  libreria con soporte real de `**` y braces, y cubrirla con los mismos tests.
- **OutputStore no reemplaza `limit`.** El `OutputStore` corta por bytes despues de
  ejecutar la tool. `limit` evita trabajo innecesario y mantiene el output
  semanticamente legible.

## 11. Recortes seguros para v1

- Se mantiene:
  - `pattern/path/limit`.
  - ripgrep `--files --glob`.
  - exclusion de `.git`.
  - sandbox por `Root`.
  - output line-oriented.
- Se omite:
  - hidden/follow.
  - MIME/type en output.
  - permisos ricos.
  - fallback sin `rg`.
  - busqueda por contenido (`grep`).
  - ordenamiento extra. Se preserva el orden del searcher; ripgrep decide.

## 12. Fuentes

- Opencode real (verificado 2026-06-22):
  - `packages/core/src/tool/glob.ts`: input `pattern/path/limit`, permiso, llamada
    a `ripgrep.glob`, output `No files found` o una ruta por linea.
  - `packages/core/src/filesystem.ts`: `GlobInput{pattern,path,limit}` y
    `FileSystem.Interface.glob`.
  - `packages/core/src/ripgrep.ts`: args `--no-config --files --glob=<pattern>
    --glob=!**/.git/** .`, normalizacion de `./`, `/` y `\`, limite por stream.
  - `packages/core/src/filesystem/schema.ts`: `Entry{path,type,mime}`.
- Atenea:
  - `internal/tool/registry.go`, `output.go`, `path.go`.
  - `internal/tool/read.go`, `write.go`, `edit.go`.
  - `app.go`.
  - Manera de trabajo: `AGENTS.md`.
