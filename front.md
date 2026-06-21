# Frontend

Este documento reúne la propuesta de interfaz y experiencia para Atenea, basada en la identidad visual y UX definida en [docs/identidad.md](docs/identidad.md).

## Principios clave
- Minimalismo y limpieza visual.
- Experiencia de chat primero, con cero fricción.
- Formas suaves y orgánicas con bordes redondeados.
- Estructura plana, sin cards tradicionales.
- Uso moderado del naranja como acento.

## Referencias
- [docs/identidad.md](docs/identidad.md)
- [docs/identidad.md](docs/identidad.md#1-concepto-principal)
- [docs/identidad.md](docs/identidad.md#2-principios-de-diseno-uxui)
- [docs/identidad.md](docs/identidad.md#3-paleta-de-colores)
- [docs/identidad.md](docs/identidad.md#4-layout-estructura-de-la-pantalla)
- [docs/identidad.md](docs/identidad.md#6-tipografia)
- [docs/identidad.md](docs/identidad.md#7-iconografia)
- [docs/identidad.md](docs/identidad.md#8-anatomia-del-mensaje-del-chat)
- [docs/identidad.md](docs/identidad.md#9-streaming-de-pensamiento-thinking-process)
- [docs/identidad.md](docs/identidad.md#10-tool-read)
- [docs/identidad.md](docs/identidad.md#11-voz-y-microcopy)

## Dirección de UI/UX
La interfaz debe sentirse accesible, fluida y sencilla, con un chat principal limpio, una sidebar persistente y un lenguaje de estado claro para comunicar progreso y control al usuario.

## Librerías recomendadas

### Core
- Vue 3
- TypeScript
- Vite
- Vue Router
- Pinia

### UI y estilos
- Tailwind CSS
- Phosphor Icons
- @fontsource/red-hat-mono

### Chat y contenido
- marked
- DOMPurify
- highlight.js o shiki

### UX y utilidades
- @vueuse/core
- GSAP
- date-fns o dayjs

### Persistencia
- pinia-plugin-persistedstate, **solo para estado de UI** (sidebar colapsada, preferencias de vista). El historial de chats no se persiste aquí: vive en el backend (ver [Persistencia y fuente de verdad](#persistencia-y-fuente-de-verdad)).

## Integración con el backend (Wails)

Atenea es una aplicación de escritorio **Wails** (Go + webview), no una SPA web. El frontend no habla con un servidor HTTP ni hace fetch/REST: se comunica con el backend Go mediante *bindings* generados y eventos del runtime de Wails. Las fases de desarrollo se construyen sobre esta superficie, no sobre llamadas HTTP.

### Bindings (acciones del usuario)
Generados en `frontend/wailsjs/go/main/App`:
- `SendPrompt(sessionID, text)`: envía un prompt a la sesión.
- `Stop(sessionID)`: interrumpe la generación en curso.

### Eventos (estado y streaming)
Vía `EventsOn` del runtime (`frontend/wailsjs/runtime/runtime`). El canal `session:<id>` emite los eventos durables del log en orden de `Seq`:
- `Text.Started` / `Text.Delta` / `Text.Ended`: streaming del texto de la IA.
- `Reasoning.Started` / `Reasoning.Delta` / `Reasoning.Ended`: pensamiento de la IA (alimenta el bloque de thinking de identidad §9).
- `Tool.Called` / `Tool.Success` / `Tool.Failed`: ejecución de herramientas (alimenta los tool states de identidad §10).
- `Step.Started` / `Step.Ended` / `Step.Failed`: ciclo de vida del paso del agente.
- Un `Message` con `Role: user` (Kind vacío) promueve el prompt del usuario al log.

Además, el canal `session:<id>:error` notifica errores duros de la corrida (fallo de proveedor, límite de pasos, stop).

> Hoy `App.vue` ya cablea una sola sesión (`sessionID = 'main'`). El store de Pinia debe formalizar este mapeo evento→estado: mensajes, streaming de texto, bloque de reasoning (últimas 4 líneas + cronómetro de §9) y estado de cada tool.

## Persistencia y fuente de verdad

- **Historial de chats:** vive en el backend Go (SQLite, `internal/session/sqlitestore.go`), que es la **única fuente de verdad**. El frontend lo lee y rehidrata desde ahí mediante bindings; no lo duplica en `localStorage`.
- **Estado de UI:** lo persiste el frontend con `pinia-plugin-persistedstate` (p. ej. la sidebar colapsada, que identidad §4 exige recordar entre sesiones).

## Ruta de desarrollo del frontend

### Fase 1: base de la aplicación
- Inicializar el proyecto con Vue 3, TypeScript y Vite.
- Configurar Tailwind CSS, Pinia, Vue Router y la fuente Red Hat Mono.
- Crear la estructura base de carpetas: componentes, stores, vistas y estilos.

### Fase 2: experiencia de chat MVP
- Construir el layout principal con un chat central y una sidebar persistente.
- Implementar el composer de mensajes y el flujo básico de envío sobre los bindings `SendPrompt` / `Stop` y los eventos del canal `session:<id>` (ver [Integración con el backend](#integracion-con-el-backend-wails)), no sobre HTTP.
- Mostrar mensajes de usuario y respuestas de IA en un flujo continuo a partir de los eventos `Text.*`.

### Fase 3: renderizado y estados visuales
- Integrar Markdown para las respuestas de la IA.
- Añadir soporte para bloques de código, tool states y estados de progreso a partir de los eventos `Tool.*` y `Step.*`.
- Definir la visualización de pensamiento (eventos `Reasoning.*`), lectura de archivos y microcopy de actividad.

### Fase 4: persistencia y refinamiento
- Persistir solo el estado de UI (sidebar colapsada, preferencias) con `pinia-plugin-persistedstate`; el historial de chats se lee del backend (ver [Persistencia y fuente de verdad](#persistencia-y-fuente-de-verdad)).
- Añadir animaciones suaves con GSAP para transiciones y microinteracciones.
- Ajustar spacing, tipografía, colores y componentes para alinearlos con la identidad visual.

### Fase 5: calidad y escalabilidad
- Añadir pruebas unitarias para componentes y stores.
- Mejorar accesibilidad, responsividad y rendimiento.
- Preparar la app para integrar nuevas capacidades del agente sin reescribir la UI.
