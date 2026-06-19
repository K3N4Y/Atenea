---
name: tdd-cycle-evidence
description: Apply a strict test-driven development workflow with evidence. Use when implementing features, fixing bugs, changing existing code, or when the user asks for TDD, RED/GREEN, a safety net, tests before code, triangulation, refactoring discipline, or progress evidence.
---

# TDD Cycle Evidence

## Overview

Usar esta skill para trabajar con un ciclo TDD verificable. Mantener el orden: Safety net, Understand, RED, GREEN, TRIANGULATE, REFACTOR, Evidence.

## Workflow

### 1. Safety Net

- Si se modifican archivos existentes, primero correr los tests actuales relevantes.
- Si fallan, reportar la falla como preexistente y no seguir tocando a ciegas.
- Registrar el comando usado y el resultado antes de editar.

### 2. Understand

- Leer la tarea, spec, escenarios de aceptacion, diseno y patrones existentes.
- Identificar el comportamiento esperado antes de escribir tests.
- Seguir las convenciones del repositorio para nombres, estructura, helpers y estilo de tests.

### 3. RED

- Escribir primero un test que falle.
- Hacer que el test describa el comportamiento esperado, no la implementacion.
- No escribir codigo de produccion antes del test.
- Ejecutar el test especifico y capturar la falla esperada.

### 4. GREEN

- Escribir el minimo codigo necesario para pasar el test.
- Ejecutar el test especifico, no toda la suite, salvo que el repositorio no permita aislarlo.
- Mantener el cambio pequeno y orientado al caso rojo.

### 5. TRIANGULATE

- Agregar casos adicionales, idealmente happy path y edge case.
- Usar estos casos para evitar falso verde por codigo hardcodeado o tests pobres.
- Ejecutar los tests especificos despues de cada caso nuevo.

### 6. REFACTOR

- Limpiar el codigo sin cambiar comportamiento.
- Despues de cada refactor, verificar que los tests sigan pasando.
- Separar refactors de cambios funcionales cuando sea posible.

## Evidence

Incluir en `apply-progress`, en el progreso equivalente y en la respuesta final una tabla `TDD Cycle Evidence`. La tabla debe mostrar como minimo RED, GREEN, TRIANGULATE y REFACTOR; incluir Safety net y Understand cuando apliquen.

Usar este formato:

| Phase | Evidence | Command or artifact | Result |
| --- | --- | --- | --- |
| Safety net | Existing tests checked | `<command>` | pass/fail/preexisting |
| Understand | Relevant files and scenarios read | `<files>` | behavior identified |
| RED | Failing test written first | `<test file>` and `<command>` | expected failure |
| GREEN | Minimal production code added | `<files>` and `<command>` | specific test passed |
| TRIANGULATE | Additional cases added | `<test file>` and `<command>` | cases passed |
| REFACTOR | Cleanup without behavior change | `<files>` and `<command>` | tests still passed |

Si algun paso no aplica, marcarlo como `N/A` y explicar por que. Si no se pueden correr tests, decirlo explicitamente y mostrar el bloqueo.
