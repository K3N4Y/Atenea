# Design: arbol de archivos en la TUI (Space+e)

Fecha: 2026-07-08  
Estado: aprobado en brainstorming; pendiente de plan de implementacion

## Objetivo

En `atenea-tui`, el atajo **Space** (leader estilo vim) + **`e`** abre o cierra un
**panel lateral izquierdo** con el arbol de archivos del workspace. El usuario
navega con teclas estilo **nvim-tree** y, al confirmar un **archivo**, se inserta
`@ruta` en el composer (misma semantica que el menu `@` actual). Los iconos de
carpetas y de lenguajes/frameworks son **Nerd Fonts** (sin fallback ASCII).

## Motivacion

Hoy el composer solo ofrece archivos en lista plana via el token `@` (filtrado
por prefijo). Un explorador jerarquico permite orientarse en el workspace sin
salir de la TUI y reutiliza el mismo contrato de menciones (`@path`) que el
agente ya entiende.

## Alcance v1

- Leader Space + `e`: toggle del panel explorador.
- Panel izquierdo; transcript + composer a la derecha.
- Arbol en memoria construido desde `listFiles` / `Engine.ProjectFiles`
  (gitignore + limite del glob, igual que el menu `@`).
- Navegacion j/k (y flechas), h/l, Enter, Esc, q.
- Insertar `@ruta` al confirmar un archivo y **cerrar** el panel.
- Iconos Nerd Font por extension y por tipo de carpeta.
- Tests de comportamiento en `internal/tui` (TDD con evidencia).

## Fuera de v1

- Filtro fuzzy / busqueda tipando en el arbol.
- Previsualizar contenido del archivo.
- Multi-select.
- Git status en el arbol.
- Acciones de FS (renombrar, borrar, crear).
- Deteccion de Nerd Font o fallback ASCII.
- Cambiar el modelo desde la TUI (pendiente ya documentado en `docs/atenea-tui.md`).

## Arquitectura

Todo el trabajo vive en `internal/tui`. No se toca el runner, Wails ni
`internal/wiring`.

| Pieza | Responsabilidad |
| --- | --- |
| `tree.go` + `tree_test.go` | Modelo de nodos, build desde rutas planas, filas visibles (expand/collapse), mapa de iconos Nerd Font |
| `model.go` | Estado del explorador y del leader; rama de teclado en `handleKey` |
| `view.go` | Layout horizontal: panel + chat; estilos lipgloss |
| `engine.go` | Sin API nueva: reutilizar `ProjectFiles` / `listFiles` de `WithCompletions` |

### Datos del arbol

1. Al **abrir** el panel (o si la lista de archivos esta vacia), invocar
   `listFiles()` (misma fuente que el `@-menu`).
2. Convertir el slice de rutas relativas planas en un arbol (`treeNode` con
   nombre, path relativo, isDir, children, expanded).
3. Las **filas visibles** se calculan recorriendo el arbol en orden de
   profundidad, solo expandiendo nodos con `expanded == true`.
4. Estado inicial al abrir: **raiz logica expandida**, carpetas hijas
   **colapsadas** (se ven solo entradas de primer nivel bajo la raiz del
   workspace). Si `listFiles` no incluye directorios vacios, las carpetas
   existen solo si tienen al menos un archivo bajo ellas (coherente con el
   glob actual).

Estructura conceptual:

```go
type treeNode struct {
    name     string
    path     string // relativo al workspace; "" para raiz virtual si hace falta
    isDir    bool
    expanded bool
    children []*treeNode
}

type fileTree struct {
    root   *treeNode
    rows   []treeRow // vista aplanada de visibles
    cursor int
}

type treeRow struct {
    node  *treeNode
    depth int
}
```

### Insertar `@ruta`

Misma idea que `applySelection` del menu de archivos:

- Insertar (o completar) el token `@` + path relativo en el caret del
  `textinput`.
- No enviar el prompt.
- Cerrar el arbol (`treeOpen = false`) tras insertar un archivo.
- Confirmar sobre una **carpeta** no inserta: solo expande (Enter / `l`).

### Leader Space

- Con el arbol **cerrado** y sin gates de permiso/plan por delante: la primera
  tecla **Space** (`tea.KeySpace` o runa espacio) **no** escribe en el input;
  pone `leaderPending = true` y agenda un timeout (~1s).
- Si llega **`e`** mientras `leaderPending`: toggle `treeOpen`, cancela leader
  y, si se abre, carga/rebuild del arbol.
- Si llega **otra tecla** o el **timeout**: cancela leader. En v1 **no** se
  reinyecta el Space ni la tecla posterior al input (comportamiento
  predecible; el usuario vuelve a escribir).
- Con el arbol **abierto**, Space+e (o solo el chord leader) cierra el panel;
  las teclas del arbol tienen prioridad sobre el leader salvo el chord de
  cierre documentado abajo.

### Prioridad de teclado (alta a baja)

1. Ctrl+C (stop + quit)
2. PgUp / PgDn (scroll del transcript)
3. Permiso pendiente (y/n)
4. Plan pendiente (y/n)
5. **Arbol abierto** (captura navegacion; no alimenta el input)
6. **Leader Space + e** (toggle arbol)
7. Menu de autocompletado abierto (`/` / `@`)
8. Esc / Enter / Tab / Shift+Tab / historial / input

## UX

### Layout

```
╭ explorer ──────╮  <transcript viewport>
│ 󰉋 cmd          │  ...
│ 󰉋 internal     │
│    model.go   │  ╭ composer ─────────╮
│    tree.go    │  │ ❯ @internal/tui…  │
│  go.mod       │  ╰───────────────────╯
╰────────────────╯  build · modelo
```

- Ancho del panel: fijo razonable (~28 celdas) o ~25% del ancho terminal, con
  minimo y maximo para terminales estrechas. Si el ancho total es muy bajo, el
  panel puede ocupar todo el ancho util (degradacion simple).
- Titulo del panel: texto estable `explorer` (asertable en tests).
- Cursor: resaltar la fila activa (estilo inverso o prefijo `>`).

### Teclas con arbol abierto

| Tecla | Accion |
| --- | --- |
| `j` / Down | Bajar cursor (clamp al final) |
| `k` / Up | Subir cursor (clamp al inicio) |
| `l` / Enter | Si dir: expandir; si archivo: insertar `@path` y cerrar |
| `h` | Si dir expandida: colapsar; si no: mover cursor al padre (si existe) |
| Esc | Cerrar panel sin insertar |
| q | Cerrar panel sin insertar |
| Space+e | Cerrar panel (toggle) |

El composer **no** recibe teclas mientras el arbol esta abierto.

### Iconos Nerd Font

Mapa en `tree.go` (constantes Unicode de Nerd Fonts). Ejemplos minimos v1:

| Clase | Glifo (ejemplo) | Notas |
| --- | --- | --- |
| Carpeta colapsada | `󰉋` | nf-md-folder |
| Carpeta expandida | `󰝰` | nf-md-folder-open |
| `.go` | `` | |
| `.ts` / `.tsx` | icono TypeScript seti/dev | |
| `.js` / `.jsx` | icono JS | |
| `.vue` | icono Vue | |
| `.md` | icono markdown | |
| `.json` / `.yaml` / `.yml` | iconos config | |
| `.css` / `.html` | iconos web | |
| default archivo | `󰈔` | nf-md-file-document-outline |

Se asume que la terminal del usuario usa una **Nerd Font**. No hay deteccion
ni fallback en v1.

Render de una fila: `indent + icon + " " + name` (indent = dos espacios por
nivel de profundidad).

## Contrato de tests

Nombres por comportamiento, tests junto al codigo (`tree_test.go`,
`model_test.go`, asserts de `View` donde aplique):

| Test | Comportamiento |
| --- | --- |
| `TestTree_BuildsFromPaths` | Rutas planas → jerarquia y paths relativos correctos |
| `TestTree_ExpandCollapseVisibleRows` | Expand/collapse cambia el conjunto de filas visibles |
| `TestTree_IconForExtension` | Extensiones conocidas devuelven el glifo esperado |
| `TestModel_LeaderSpaceE_OpensTree` | Space luego e abre el panel |
| `TestModel_LeaderSpaceE_TogglesClosed` | Con arbol abierto, Space+e lo cierra |
| `TestModel_TreeKeys_NavigateAndInsertAt` | j/k mueven; Enter en archivo inserta `@path` y cierra |
| `TestModel_TreeOpen_CapturesKeyboard` | Con arbol abierto, runas no van al textinput |
| (vista) | Con arbol abierto, `View()` contiene el marcador `explorer` |

Los tests de teclado usan `tea.KeyMsg` como el resto de `model_test.go`
(incluyendo `KeySpace` para el espacio, ya usado en el suite).

## Errores y bordes

- `listFiles` nil o error: panel abre vacio o con una linea de error tenue;
  no panic.
- Workspace sin archivos: panel con solo mensaje vacio / sin filas.
- Timeout del leader: cancela sin efectos laterales.
- Permiso o plan pendiente: el leader y el arbol **no** interceptan (gates
  existentes primero).
- Terminal 0x0 / muy estrecha: sin panic; dimensiones acotadas como el resto
  del viewport.

## Impacto en docs

Actualizar `docs/atenea-tui.md` en la implementacion: teclas del explorer,
leader Space, y quitar o anotar el pendiente si aplica.

## Criterios de exito

1. Space+e abre el panel izquierdo con arbol del workspace.
2. Space+e de nuevo (o Esc/q) lo cierra.
3. Enter/l sobre un archivo deja `@ruta` en el composer y cierra el panel.
4. Iconos Nerd Font visibles en terminal con esa fuente.
5. Suite de tests del paquete `internal/tui` verde; `gofmt` y `go vet` limpios.
