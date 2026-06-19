# Arquitectura de OpenCode

Investigado el 2026-06-19 sobre la documentacion oficial de OpenCode y el
repositorio `anomalyco/opencode` en la rama `dev`. En esa rama los paquetes
principales consultados reportan version `1.17.8`.

## Resumen

OpenCode es un agente de programacion con varias interfaces sobre un mismo
backend. La idea arquitectonica central es separar:

- **Clientes**: TUI, CLI no interactiva, web app, desktop app, extensiones de
  IDE y SDK.
- **Servidor local/headless**: expone una API HTTP/OpenAPI y streams de eventos.
- **Core del agente**: sesiones, mensajes, contexto del proyecto, proveedores
  LLM, permisos, herramientas, persistencia y eventos.
- **Extensiones**: configuracion JSON/JSONC, agentes especializados, MCP,
  plugins, herramientas custom, skills, LSP y formatters.

En uso normal, `opencode` arranca la TUI y tambien un servidor. La TUI funciona
como cliente de ese servidor. En modo headless, `opencode serve` levanta solo el
servidor HTTP para que otro cliente lo controle. En modo browser, `opencode web`
levanta el servidor y abre una web app local.

## Vista de alto nivel

```text
Usuario
  |
  | opencode / opencode run / opencode web / opencode serve / SDK
  v
Clientes
  - TUI terminal
  - CLI run
  - Web app
  - Desktop app
  - IDE extension
  - @opencode-ai/sdk
  |
  | HTTP + OpenAPI + SSE/eventos + endpoint TUI
  v
Servidor OpenCode
  - sesiones y mensajes
  - configuracion y providers
  - permisos
  - herramientas
  - archivos, busqueda, VCS, LSP, formatters, MCP
  - eventos
  |
  v
Core del agente
  - contexto de proyecto
  - runner de sesion
  - proveedor/modelo LLM
  - ejecucion de tool calls
  - persistencia local
```

## Monorepo y paquetes relevantes

El repo upstream es un monorepo. Las carpetas importantes para entender la
arquitectura son:

| Paquete | Rol |
| --- | --- |
| `packages/opencode` | Ensambla el binario `opencode`. Contiene comandos CLI, servidor HTTP, integracion con TUI y dependencias principales del runtime. |
| `packages/core` | Dominio central: agentes, configuracion, credenciales, base de datos, eventos, filesystem, permisos, proyecto, PTY, sesiones, tools, snapshots y workspace. |
| `packages/llm` | Capa de proveedores y protocolos LLM. Exporta adaptadores para Anthropic, OpenAI, Bedrock, Gemini, OpenRouter y protocolos compatibles. |
| `packages/server` | Tipos/utilidades compartidas del servidor. La implementacion HTTP usada por el binario vive principalmente bajo `packages/opencode/src/server`. |
| `packages/sdk/js` | SDK JS/TS generado desde la especificacion OpenAPI. Expone `@opencode-ai/sdk` para crear o adjuntarse a un servidor. |
| `packages/tui` | Interfaz terminal. Usa OpenTUI/Solid y consume el SDK para hablar con el backend. |
| `packages/app` | Web app Solid/Vite usada por el modo web y por empaquetados visuales. |
| `packages/desktop` | Empaquetado Electron para la experiencia desktop. |
| `packages/plugin` | Tipos/base para plugins. |
| `packages/web` | Sitio publico/documentacion de OpenCode, distinto de la app runtime. |

La separacion importante es que `core` concentra el dominio del agente, mientras
que `opencode`, `tui`, `app`, `desktop` y `sdk` son capas de entrada/salida.

## Servidor como contrato principal

OpenCode documenta el servidor como la forma programatica de interactuar con el
agente. `opencode serve` expone HTTP en `127.0.0.1:4096` por defecto, con flags
para puerto, hostname, CORS y mDNS. Tambien soporta autenticacion HTTP Basic por
medio de `OPENCODE_SERVER_PASSWORD` y `OPENCODE_SERVER_USERNAME`.

El servidor publica una especificacion OpenAPI 3.1 en `/doc`. Esa especificacion
se usa para inspeccionar tipos, generar clientes y alimentar el SDK JS/TS.

APIs principales expuestas:

- `GET /global/health` y `GET /global/event`: salud y eventos globales.
- `GET /project`, `GET /project/current`: proyectos.
- `GET /path`, `GET /vcs`: directorio actual y estado de VCS.
- `GET/PATCH /config`: configuracion efectiva.
- `GET /provider`, auth de providers y modelos disponibles.
- `GET/POST/PATCH/DELETE /session`: ciclo de vida de sesiones.
- `POST /session/:id/message`, `prompt_async`, `command`, `shell`: ejecucion de
  prompts, comandos slash y shell.
- `GET /session/:id/message`: historial de mensajes.
- `GET /session/:id/diff`, `revert`, `unrevert`: diffs y reversibilidad.
- `GET /find`, `/find/file`, `/find/symbol`, `/file/content`, `/file/status`:
  busqueda y lectura de archivos.
- `GET /experimental/tool`: schemas de tools disponibles.
- `GET /lsp`, `/formatter`, `/mcp`: estado de LSP, formatters y MCP.
- `GET /agent`: agentes disponibles.
- `/tui/*`: control remoto de la TUI, usado por integraciones IDE.
- `GET /event`: stream SSE de eventos del servidor.

Internamente, el servidor upstream usa `effect`, `@effect/platform-node`,
`HttpApi`, `OpenApi`, `NodeHttpServer`, un tracker de WebSockets y mDNS opcional.

## Flujo de ejecucion de una sesion

1. El usuario arranca `opencode`, `opencode run`, `opencode web` o un cliente
   SDK.
2. La CLI resuelve directorio, configuracion, provider/modelo, agente y permisos.
3. Se crea o se reanuda una sesion. Una sesion puede continuarse, bifurcarse,
   compartirse, resumirse, resumirse como fork, revertirse o compactionarse.
4. El servidor arma el contexto: mensajes previos, archivos referenciados,
   instrucciones del proyecto, configuracion del agente, modelo, permisos y
   tools disponibles.
5. El runner invoca el modelo por medio de la capa LLM/provider.
6. Si el modelo pide una herramienta, el servidor evalua permisos (`allow`,
   `ask`, `deny`). Si procede, ejecuta la tool y agrega el resultado como parte
   del mensaje.
7. El bus de eventos emite cambios de sesion, mensaje, tool, permiso y estado.
   La TUI/web/SDK renderizan esos eventos.
8. Cuando la sesion vuelve a `idle`, el cliente termina el prompt o queda listo
   para la siguiente interaccion.

## Agentes

OpenCode distingue dos tipos:

- **Agentes primarios**: son la personalidad/modo principal de una conversacion.
  Los integrados mas importantes son `build` y `plan`.
- **Subagentes**: los invoca un agente primario para tareas especializadas o se
  mencionan manualmente con `@`. Los integrados incluyen `general`, `explore` y
  `scout`.

El modelo de agentes esta conectado con permisos. `build` tiene acceso amplio
para trabajo de desarrollo. `plan` esta restringido para analisis y planificacion
sin modificar el repositorio por defecto. `explore` y `scout` son de lectura para
exploracion local o investigacion externa.

## Herramientas y permisos

Las tools son el mecanismo por el que el LLM actua sobre el entorno. OpenCode
incluye herramientas para:

- `bash`: ejecutar comandos.
- `edit`, `write`, `apply_patch`: modificar archivos.
- `read`: leer archivos.
- `grep`, `glob`: buscar contenido o rutas.
- `lsp`: consultar lenguaje/IDE cuando esta habilitado.
- `skill`: cargar skills.
- `todowrite`: gestionar listas de tareas.
- `webfetch`, `websearch`: investigar en web.
- `question`: pedir informacion al usuario.
- Tools externas via MCP o custom tools.

La configuracion `permission` decide si una accion corre sin aprobacion, pide
aprobacion o queda bloqueada. Puede ser global (`"*": "ask"`), por herramienta
(`"bash": "allow"`) o granular por patron (`"rm *": "deny"`). Tambien existe
`external_directory` para permitir accesos fuera del workspace actual.

## Configuracion

OpenCode usa JSON/JSONC. Los archivos de configuracion se fusionan por capas; las
capas posteriores sobrescriben claves conflictivas:

1. Config remota organizacional desde `.well-known/opencode`.
2. Config global en `~/.config/opencode/opencode.json`.
3. `OPENCODE_CONFIG`.
4. Config de proyecto `opencode.json`.
5. Directorios `.opencode/` para agentes, comandos, plugins, skills, tools y
   temas.
6. `OPENCODE_CONFIG_CONTENT`.
7. Config administrada del sistema operativo.
8. Preferencias administradas por MDM en macOS.

La config cubre servidor, shell, modelos, providers, agentes, permisos,
formatters, LSP, MCP, plugins, instrucciones, temas, keybinds, sharing,
compaction y flags experimentales.

## Providers LLM

OpenCode usa AI SDK y Models.dev para soportar muchos providers. La capa de
providers separa credenciales, seleccion de modelo y detalles de protocolo. Las
credenciales agregadas con `/connect` se guardan localmente en
`~/.local/share/opencode/auth.json`.

Tambien se pueden configurar providers personalizados o endpoints compatibles,
por ejemplo ajustando `baseURL` en la seccion `provider` del config.

## Extensibilidad

OpenCode se extiende principalmente por cuatro vias:

- **MCP**: servers locales o remotos agregan tools al contexto del modelo. Se
  configuran bajo `mcp` y pueden habilitarse/deshabilitarse por servidor.
- **Plugins**: modulos JS/TS cargados desde `.opencode/plugins/`,
  `~/.config/opencode/plugins/` o npm. Pueden engancharse a eventos como
  `tool.execute.before`, `tool.execute.after`, `session.updated`,
  `permission.asked`, `file.edited`, etc.
- **Agentes custom**: archivos de agente con prompt, modo, modelo, permisos y
  metadata.
- **Tools/skills custom**: mecanismos locales para agregar capacidades
  controladas al agente.

La advertencia practica para MCP es que cada servidor agrega herramientas y
schemas al contexto, por lo que puede consumir tokens rapidamente si se habilitan
demasiados servers.

## Integracion desde este proyecto Wails

Este repo actual es una app Wails Go + Vue. Si se quisiera integrar OpenCode, las
opciones mas sanas son:

1. **Sidecar local via proceso**: Go arranca `opencode serve` o `opencode web`,
   gestiona el ciclo de vida del proceso y la UI Vue consulta el backend por una
   API propia en Go. Es la opcion mas alineada con Wails.
2. **Cliente HTTP directo**: Go llama la API HTTP/OpenAPI de OpenCode. Evita
   meter Node/Bun en el frontend y permite controlar auth, CORS y paths desde el
   backend nativo.
3. **SDK JS/TS**: util si una parte Node/Bun existe en la app. En un Wails puro,
   el SDK en frontend no es ideal porque el browser queda expuesto a CORS/auth y
   al ciclo de vida del proceso.
4. **CLI simple**: para tareas puntuales, ejecutar `opencode run "..."` desde Go
   y capturar stdout es lo mas simple, aunque da menos control sobre sesiones,
   eventos y tools.

Para una integracion seria, trataria a OpenCode como backend externo controlado
por contrato HTTP, no como libreria embebida. El core upstream esta hecho para el
runtime TypeScript/Bun y cambia mas que una API OpenAPI versionada.

## Fuentes consultadas

- Documentacion principal: https://opencode.ai/docs/
- Servidor y API: https://opencode.ai/docs/server/
- SDK: https://opencode.ai/docs/sdk/
- CLI: https://opencode.ai/docs/cli/
- TUI: https://opencode.ai/docs/tui/
- Web: https://opencode.ai/docs/web/
- IDE: https://opencode.ai/docs/ide/
- Config: https://opencode.ai/docs/config/
- Agents: https://opencode.ai/docs/agents/
- Tools: https://opencode.ai/docs/tools/
- Permissions: https://opencode.ai/docs/permissions/
- MCP: https://opencode.ai/docs/mcp-servers/
- Plugins: https://opencode.ai/docs/plugins/
- Repo: https://github.com/anomalyco/opencode
- Monorepo packages: https://github.com/anomalyco/opencode/tree/dev/packages
- Core source layout: https://github.com/anomalyco/opencode/tree/dev/packages/core/src
- Runtime source layout: https://github.com/anomalyco/opencode/tree/dev/packages/opencode/src
