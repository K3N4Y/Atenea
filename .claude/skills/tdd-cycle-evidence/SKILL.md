---
name: tdd-cycle-evidence
description: Apply a strict test-driven development workflow with evidence, orchestrating subagents per phase. Use when implementing features, fixing bugs, changing existing code, or when the user asks for TDD, RED/GREEN, a safety net, tests before code, triangulation, refactoring discipline, or progress evidence.
---

# TDD Cycle Evidence (Orquestador)

## Overview

Esta skill convierte al agente principal en un **orquestador** del ciclo TDD
verificable. El orquestador ejecuta directamente los pasos 1 (Safety net) y 2
(Understand), y luego **delega cada una de las fases restantes a un subagente
dedicado**: una fase, un subagente. Entre fase y fase el orquestador aplica un
**gate estricto**: re-verifica la evidencia antes de lanzar la siguiente.

Mantener el orden: Safety net, Understand, RED, GREEN, TRIANGULATE, REFACTOR,
Evidence.

```
[Orquestador]  1. Safety net  ->  2. Understand
                                       |
                                       v   (brief + gate por fase)
[Subagente] 3. RED  ->[gate]-> [Subagente] 4. GREEN ->[gate]->
[Subagente] 5. TRIANGULATE ->[gate]-> [Subagente] 6. REFACTOR ->[gate]
                                       |
                                       v
[Orquestador]  Evidence (tabla consolidada)
```

## Responsabilidades del orquestador

El orquestador **no escribe el test ni el codigo de produccion**. Su trabajo es:

1. Ejecutar Safety net y Understand (pasos 1 y 2).
2. Preparar un **brief** preciso para cada fase delegada.
3. Lanzar un subagente por fase con el `Agent` tool, en orden estricto.
4. Aplicar el **gate**: re-correr o inspeccionar la evidencia que devuelve el
   subagente. Si no cumple, rechazar, corregir el brief y relanzar.
5. Consolidar la tabla `TDD Cycle Evidence` al final.

Las fases delegadas son secuenciales: cada gate debe pasar antes de delegar la
siguiente. Nunca lanzar fases en paralelo (cada fase depende del estado de
archivos que dejo la anterior).

## Pasos que ejecuta el orquestador

### 1. Safety net (orquestador)

- Si se modifican archivos existentes, primero correr los tests actuales
  relevantes.
- Si fallan, reportar la falla como preexistente y no seguir tocando a ciegas.
- Registrar el comando usado y el resultado antes de delegar nada.

### 2. Understand (orquestador)

- Leer la tarea, spec, escenarios de aceptacion, diseno y patrones existentes.
- Identificar el comportamiento esperado antes de delegar.
- Anotar las convenciones del repositorio para nombres, estructura, helpers y
  estilo de tests (`foo.go` -> `foo_test.go`, nombres por comportamiento como
  `TestRunner_StopsAtStepLimit`, frontera Wails en `internal/event`, etc.).
- El producto de este paso es el **brief base** que se inyecta en cada fase.

## Pasos delegados (un subagente por fase)

Para cada fase, el orquestador lanza un subagente con `Agent`
(`subagent_type: general-purpose`) y le pasa el brief. El subagente **debe
devolver siempre**: archivos tocados, comando(s) exacto(s) corrido(s), salida
cruda relevante del test, y una linea de resultado.

### Brief base (inyectar en cada fase)

```
Tarea: <que se construye o arregla>
Comportamiento esperado: <del paso Understand>
Convenciones: tests junto al codigo (_test.go), nombre por comportamiento,
  -race en codigo concurrente, frontera Wails en internal/event.
Fase actual: <RED|GREEN|TRIANGULATE|REFACTOR> -- hacer SOLO esta fase.
Estado previo: <archivos y resultados que dejaron las fases anteriores>
Devolver: archivos tocados, comando(s) exacto(s), salida cruda del test, resultado.
```

### 3. RED (subagente)

- Escribir primero un test que falle.
- Hacer que el test describa el comportamiento esperado, no la implementacion.
- No escribir codigo de produccion.
- Ejecutar el test especifico y capturar la falla esperada
  (`go test -run TestName -v ./internal/...`).

**Gate RED (orquestador):** re-correr el test especifico. El gate pasa solo si
el test existe, se ejecuta y **falla por la razon esperada** (asercion del
comportamiento, no un error de compilacion ajeno ni otro test). Si pasa en
verde de entrada o falla por otra causa, rechazar y relanzar.

### 4. GREEN (subagente)

- Escribir el minimo codigo de produccion para pasar el test rojo.
- Ejecutar el test especifico, no toda la suite, salvo que no se pueda aislar.
- Mantener el cambio pequeno y orientado al caso rojo.

**Gate GREEN (orquestador):** re-correr el test especifico y confirmar que
**pasa**. Correr ademas un chequeo rapido de que no se rompio nada cercano
(`go test ./...` del paquete afectado). Si el test sigue rojo o el cambio
excede lo minimo, rechazar y relanzar.

### 5. TRIANGULATE (subagente)

- Agregar casos adicionales: happy path y edge case.
- Usar estos casos para evitar falso verde por codigo hardcodeado o tests
  pobres.
- Ejecutar los tests especificos despues de cada caso nuevo (con `-race` si el
  codigo es concurrente).

**Gate TRIANGULATE (orquestador):** re-correr los casos nuevos y confirmar que
pasan. Verificar que sean significativos (que tumbarian una implementacion
hardcodeada, no triviales). Si los casos son redundantes o no aportan,
rechazar y relanzar.

### 6. REFACTOR (subagente)

- Limpiar el codigo sin cambiar comportamiento.
- Separar refactors de cambios funcionales cuando sea posible.
- Verificar que los tests sigan pasando despues de cada refactor.

**Gate REFACTOR (orquestador):** correr las puertas de calidad de cierre:
`gofmt -l .` (debe salir vacio), `go vet ./...` (limpio) y `go test ./...`
(toda la suite verde). Si algo falla, rechazar y relanzar.

## Evidence (orquestador)

El orquestador consolida la evidencia de todas las fases (la propia mas la que
devolvio cada subagente) en una tabla `TDD Cycle Evidence`. Incluirla en
`apply-progress`, en el progreso equivalente y en la respuesta final. La tabla
debe mostrar como minimo RED, GREEN, TRIANGULATE y REFACTOR; incluir Safety net
y Understand cuando apliquen. Marcar en cada fase delegada el resultado del gate.

Usar este formato:

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Existing tests checked (orquestador) | `<command>` | pass/fail/preexisting |
| Understand | Relevant files and scenarios read (orquestador) | `<files>` | behavior identified |
| RED | Failing test written first (subagente) + gate | `<test file>` and `<command>` | expected failure, gate ok |
| GREEN | Minimal production code added (subagente) + gate | `<files>` and `<command>` | specific test passed, gate ok |
| TRIANGULATE | Additional cases added (subagente) + gate | `<test file>` and `<command>` | cases passed, gate ok |
| REFACTOR | Cleanup without behavior change (subagente) + gate | `<files>` and `gofmt -l .`, `go vet ./...`, `go test ./...` | tests still passed, gate ok |

Si algun paso no aplica, marcarlo como `N/A` y explicar por que. Si un gate no
se pudo correr (tests imposibles de ejecutar), decirlo explicitamente y mostrar
el bloqueo en vez de marcar el gate como ok.
