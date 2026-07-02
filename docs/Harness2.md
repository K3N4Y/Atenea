# Harness2: Investigacion sobre SkillOpt (Microsoft)

Investigacion sobre **SkillOpt**, el optimizador de skills en espacio de texto
publicado por Microsoft Research. Documento hermano de `Harness.md` (que contiene
el paper *Self-Harness*); al final se comparan ambos enfoques y se discute su
relevancia para `atenea`.

- Paper: *SkillOpt: Executive Strategy for Self-Evolving Agent Skills*,
  Yang et al., 2026. arXiv:2605.23904 (v1 22-may-2026, v2 25-may-2026).
  27 paginas, 4 figuras, 6 tablas. Subjects: cs.AI, cs.CL.
- Codigo: https://github.com/microsoft/SkillOpt (licencia MIT).
- Project page: https://microsoft.github.io/SkillOpt/ (alias aka.ms/skillopt).
- Equipo liderado por Microsoft Research, con autores de Microsoft, Shanghai Jiao
  Tong University, Tongji University y Fudan University.

> Nota de fiabilidad: el paper y varias fuentes estan fechados en 2026 y citan
> modelos como GPT-5.5 / GPT-5.4 y Qwen3.5/3.6. Las cifras provienen del paper,
> la project page y articulos de prensa (VentureBeat, AI Papers Academy). Donde
> una afirmacion viene de una fuente de terceros con interes comercial (p. ej.
> CodexOpt) se marca explicitamente.

## 1. Resumen ejecutivo

SkillOpt es, segun los autores, el **primer optimizador de skills sistematico y
controlable en espacio de texto** para agentes LLM. La idea central: tratar un
documento de skill en lenguaje natural (un `.md` compacto) como el **estado
entrenable** de un agente con modelo congelado, y "entrenarlo" con la misma
disciplina que el entrenamiento de redes neuronales: epochs, mini-batches,
learning rate y validation gates, pero **sin tocar los pesos del modelo**.

Lema del proyecto: **"Train the procedure, not the weights"** (entrena el
procedimiento, no los pesos).

El artefacto desplegable es un unico `best_skill.md` (tipicamente 300-2.000
tokens, mediana ~920) que se consume contra el modelo objetivo sin cambios y
**no anade ninguna llamada extra al modelo en tiempo de inferencia**.

Resultado titular: en 6 benchmarks x 7 modelos objetivo x 3 harnesses de
ejecucion (chat directo, Codex, Claude Code), SkillOpt es **el mejor o empata en
las 52 celdas evaluadas** (modelo, benchmark, harness), batiendo a todos los
competidores por celda: skills humanas, one-shot LLM, Trace2Skill, TextGrad,
GEPA y EvoSkill.

## 2. El problema que resuelve

Hoy las skills de agentes se construyen de tres formas, y ninguna se comporta
como un optimizador de aprendizaje profundo:

1. **Hechas a mano** (hand-crafted) por expertos humanos.
2. **Generadas one-shot** por un LLM.
3. **Evolucionadas por auto-revision poco controlada** (self-revision).

Ninguna mejora de forma fiable sobre su punto de partida bajo feedback. Los
autores argumentan que la skill deberia entrenarse como **estado externo de un
agente congelado**, con la misma reproducibilidad que la optimizacion en espacio
de pesos. SkillOpt llena ese hueco.

## 3. Idea central: la skill como estado entrenable (analogia con deep learning)

| Concepto en deep learning | Equivalente en SkillOpt |
| --- | --- |
| Pesos del modelo | El documento de skill (`skill.md`) |
| Gradiente | Las ediciones propuestas (add/delete/replace) |
| Forward pass | **Rollout**: el agente congelado ejecuta tareas con la skill actual |
| Backward pass | **Reflect**: un modelo optimizador analiza trayectorias y propone ediciones |
| Learning rate | El **presupuesto de ediciones** por iteracion (textual learning rate; lr=4 por defecto) |
| Mini-batch | Subconjunto de rollouts puntuados que se pasan al optimizador |
| Validation gate | Aceptar la edicion solo si mejora un **score held-out** |
| Epoch | Pasada completa que dispara el **slow/meta update** |

Puntos clave de la analogia:

- La skill juega el papel de los pesos; las ediciones son como gradientes que
  sugieren como cambiar los "parametros".
- El numero de ediciones permitidas por iteracion actua como learning rate.
- El modelo objetivo, el backend y el harness permanecen **fijos**; solo cambia
  el documento de skill.

## 4. Como funciona: el loop de optimizacion

El pipeline completo es: **rollout -> reflect -> aggregate -> select -> update
-> evaluate**. Se descompone en dos caminos: el *fast update* (continuo) y el
*slow update* (una vez por epoch).

### 4.1 Fast update path (continuo)

1. **Split del dataset** en train / validation / test. El agente fijo opera con
   el archivo de skill.
2. **Rollout**: cada iteracion procesa un batch de muestras de train con la skill
   actual, produciendo trayectorias de ejecucion (traces) y salidas finales.
   Equivale al *forward pass*.
3. **Reflect (optimizer analysis)**: los rollouts se dividen en mini-batches y se
   pasan a un **modelo optimizador** (un LLM frontera fuerte; GPT-5.5 en el
   paper). Se analiza la **trayectoria completa** (uso de herramientas, pasos
   intermedios y salida final), no solo la respuesta. El optimizador propone
   ediciones: **replace, remove o add** reglas. Es un "backward pass a nivel de
   lenguaje". Los exitos y fracasos se reflejan por separado, para corregir
   errores recurrentes preservando lo que ya funciona.
4. **Aggregate (consolidacion y ranking)**: un segundo paso consolida y rankea
   todas las ediciones candidatas a traves de los mini-batches.
5. **Select (textual learning rate)**: solo avanza un numero limitado de las
   ediciones mejor rankeadas. Evita reescrituras destructivas y "updates
   demasiado agresivos que desestabilizarian la optimizacion".
6. **Gate (validation gate)**: las ediciones seleccionadas crean una skill
   candidata; el agente corre sobre el **validation set held-out**. Si mejora, la
   edicion se acepta; si no, se rechaza y se conserva la skill anterior. Las
   ediciones fallidas se muestran al optimizador en iteraciones futuras (ver
   rejected-edit buffer) para no repetir cambios inutiles.

### 4.2 Slow update path (una vez por epoch)

Mira patrones de mas largo alcance a lo largo de muchas iteraciones:

1. **Comparacion inicio vs fin de epoch**: las mismas muestras de train se
   procesan dos veces, una con la skill del inicio del epoch y otra con la skill
   tras el fast-update.
2. **Categorizacion de resultados** en cuatro grupos:
   - **Improvements**: antes fallaba, ahora resuelve.
   - **Regressions**: antes resolvia, ahora falla.
   - **Persistent Failures**: falla en ambas.
   - **Stable Successes**: acierta en ambas.
3. **Reflexion por epoch**: el optimizador busca patrones de alto nivel (que
   ayuda, que perjudica, que modos de fallo persisten) y modifica una **porcion
   dedicada de la skill** que el fast path no toca. Estos cambios tambien pasan
   el validation gate.
4. **Meta-skill / memoria de largo plazo**: registro de que ediciones funcionaron,
   cuales fallaron y que retos siguen sin resolver, para guiar epochs futuros.

Tras varios epochs se obtiene la skill final optimizada.

### 4.3 Figuras del paper (descritas)

- **Teaser**: modelo objetivo, modelo optimizador, ediciones acotadas, validation
  gate y la best skill exportada.
- **Pipeline**: rollout, reflection, ediciones acotadas, validation gate, slow
  update y meta skill.
- **Epoch trends**: compara checkpoints "selection-best" contra el score de
  rollout en train y el rendimiento en test no visto.
- **ALFWorld evolution**: train rollout vs score del selection gate, con las
  ediciones rechazadas dibujadas como puntos hacia abajo.

## 5. Mecanismos clave de estabilidad

- **Textual learning rate (presupuesto de ediciones)**: acota cuanto puede
  cambiar la skill por iteracion. Previene reescrituras amplias que perderian
  reglas utiles, manteniendo plasticidad para aprender procedimientos nuevos.
  Valor por defecto `lr=4`.
- **Gated held-out selection**: convierte la reflexion en optimizacion
  *propose-and-test* en lugar de auto-edicion incondicional. Una edicion solo se
  acepta si **mejora estrictamente** el score held-out.
- **Rejected-edit buffer**: las ediciones rechazadas se convierten en feedback
  negativo, para que el optimizador evite "repetir direcciones daninas".
- **Slow update + meta skill**: feedback de horizonte largo (por epoch) sin
  inflar el artefacto desplegado.

Resultado neto: estas piezas hacen estable el entrenamiento de skills **sumando
cero llamadas al modelo en tiempo de inferencia** en el despliegue.

## 6. El artefacto: `best_skill.md`

- SkillOpt exporta un **unico fichero compacto** `best_skill.md`.
- Tamaño tipico 300-2.000 tokens; en todos los benchmarks nunca supero los 2.000,
  con mediana ~920 tokens.
- Legible y auditable: un humano puede revisarlo y gestionarlo en minutos.
- En despliegue, **el modelo objetivo consume solo la skill final, no la memoria
  del optimizador**.

## 7. Setup experimental

- **6 benchmarks**: SearchQA, SpreadsheetBench (Sheet), Office/OfficeQA, DocVQA,
  LiveMathBench, ALFWorld.
- **7 modelos objetivo**: familia GPT-5.x (incl. GPT-5.5, GPT-5.4, GPT-5.4-mini,
  GPT-5.4-nano) y familia Qwen (Qwen3.5-4B, Qwen3.6-35B-A3B), entre otros.
- **3 harnesses de ejecucion**: chat directo, Codex (CLI agentico), Claude Code
  (CLI agentico).
- **Optimizador**: un LLM frontera fuerte (GPT-5.5 en el paper); puede diferir
  del modelo objetivo o ser el mismo (self-optimizer).
- **Baselines comparados**: skills humanas, one-shot LLM, Trace2Skill, TextGrad,
  GEPA, EvoSkill.

## 8. Resultados

### 8.1 Barrido limpio (52/52)

SkillOpt es **el mejor o empata en las 52 celdas** (modelo x benchmark x harness)
y bate a cada competidor por celda.

### 8.2 Ganancias de accuracy (sobre baseline sin skill)

Sobre **GPT-5.5**, ganancia media sobre el baseline sin skill:

| Harness | Ganancia |
| --- | --- |
| Chat directo | +23,5 puntos |
| Codex (loop agentico) | +24,8 puntos |
| Claude Code | +19,1 puntos |

(La project page reporta ademas +21,8 para GPT-5.5 en Codex en otra agregacion;
las cifras exactas varian segun la tabla concreta.)

### 8.3 Ganancias por benchmark (ejemplos)

| Benchmark | Sin skill | Con SkillOpt |
| --- | --- | --- |
| ALFWorld | 83,6 | 95,5 |
| SpreadsheetBench | 41,8 | 80,7 (~2x) |
| OfficeQA | 33,1 | 72,1 |

### 8.4 Modelos pequenos

Las ganancias no se limitan a modelos frontera:

- GPT-5.4-nano: +24,9 de media.
- Qwen3.5-4B: +19,2.
- Qwen3.6-35B-A3B: +9,1.

### 8.5 Ejemplo de evolucion de skill (ALFWorld)

Target GPT-5.4-mini, optimizador GPT-5.5. El selection score subio de 68,6% a
81,4%; el test "hard" final mejoro de 70,9% a 85,8%. Un "slow update" en el epoch
3 rescato una candidata; un step intermedio entreno mas alto pero fallo el
selection gate. Reglas aprendidas: ampliar la busqueda tras varios fallos
seguidos y mantener un conjunto numerado de elementos ya buscados.

## 9. Transferibilidad

Una de las propiedades mas notables: la skill exportada se comporta como un
**artefacto reutilizable** que transfiere sin re-optimizar el lado del target.

- **Cross-model** (entre escalas de modelo): skill de LiveMath en GPT-5.4
  llevada a GPT-5.4-nano -> +15,2. Una skill optimizada en un modelo pequeno da
  un buen punto de partida para uno grande, reduciendo el coste de optimizar en
  una flota.
- **Cross-harness**: skill de SpreadsheetBench entrenada en Codex llevada a
  Claude Code -> +31,8.
- **Self-optimizer**: GPT-5.4-nano como su propio optimizador -> +10,4. Esto
  demuestra que el loop **no es mera destilacion desde un modelo mas fuerte**:
  incluso con target = optimizador encuentra ediciones utiles cuando estan
  acotadas, bufferizadas y validadas.
- **Cross-benchmark**: transfiere a un benchmark de matematicas cercano sin
  optimizacion adicional, con ganancias consistentes aunque modestas.

Matiz honesto (segun AI Papers Academy): la transferibilidad "funciona, pero no
es muy consistente"; la preservacion entre variantes de modelo es desigual y las
ganancias cross-benchmark no son enormes.

## 10. Eficiencia y costes

- **Artefactos compactos y auditables**: <= 2.000 tokens, mediana ~920.
- **Coste de entrenamiento**: el paper menciona que los tokens de entrenamiento
  pueden llegar a ~210 millones en benchmarks academicos; para casos de uso
  empresariales del dia a dia es mucho mas ligero.
- **Overhead inherente**: el metodo requiere rollouts repetidos del agente mas un
  optimizador frontera (GPT-5.5), lo que implica coste no trivial durante la
  optimizacion (aunque cero en inferencia de despliegue).

## 11. Disponibilidad y uso practico

### 11.1 Instalacion

```bash
pip install skillopt        # SkillOpt v0.1.0 en PyPI; requiere Python 3.10+; MIT
```

El loop completo (rollout -> reflect -> aggregate -> select -> update ->
evaluate) viene incluido.

### 11.2 Backends soportados (multi-backend)

OpenAI, Azure, Claude, Qwen y MiniMax. Ejemplos de backends:
`openai_chat`, `claude_chat`, `qwen_chat`, `minimax_chat`, `codex_exec`,
`claude_code_exec`.

Anadir un backend: crear `skillopt/model/<name>_backend.py`, registrarlo en
`skillopt/model/common.py` y `backend_config.py`, y enrutar via
`skillopt/model/__init__.py`. Plantillas: `qwen_backend.py`, `minimax_backend.py`.

### 11.3 Benchmarks (envs)

Seis benchmarks integrados. Un benchmark es un paquete `skillopt/envs/<name>/`
con `dataloader.py`, `rollout.py` y un `initial.md` (skill semilla). La
referencia mas simple es `skillopt/envs/searchqa/`.

### 11.4 WebUI dashboard

```bash
pip install -e ".[webui]"
python -m skillopt_webui.app
```

| Flag | Default | Descripcion |
| --- | --- | --- |
| `--port` | 7860 | Puerto del servidor |
| `--host` | `0.0.0.0` | Direccion de bind |
| `--share` | off | Crea un enlace publico de Gradio |

### 11.5 Estructura del repo

Directorios notables: `ckpt`, `configs`, `data`, `docs`, `plugins`, `scripts`,
`skillopt`, `skillopt_sleep`, `skillopt_webui`, `tests`. Lenguajes: Python
~87%, HTML ~12%, Shell ~1%.

### 11.6 SkillOpt-Sleep (preview)

Companion de **auto-evolucion offline nocturna** para coding agents locales
(Claude Code / Codex / Copilot). Revisa sesiones pasadas, re-ejecuta tareas
recurrentes y consolida skills validadas detras de un held-out gate. Documentado
en `docs/sleep/README.md`. Muy relevante para un harness que quiera mejorar sus
propias skills entre sesiones.

## 12. Aplicacion a coding agents: CodexOpt

> CodexOpt es un proyecto de terceros (Superagentic AI) que adapta las ideas de
> SkillOpt a Codex. Util como referencia de integracion, pero sus cifras no estan
> verificadas de forma independiente.

En coding agents, los ficheros de instrucciones (`AGENTS.md`, `SKILL.md`) se
tratan como **componentes vivos del runtime**, no como notas pasivas: Codex los
incorpora directamente en el loop del agente, generando trayectorias de ejecucion
observables que son la materia prima de la optimizacion.

Pipeline de rollout de CodexOpt:

1. Desplegar una skill candidata.
2. Ejecutar tareas via `codex exec`.
3. Capturar streams de eventos JSON y resultados.
4. Puntuar con verificadores, jueces LLM o analisis estatico.
5. Generar reescrituras acotadas.
6. Validar en tareas held-out antes de aceptar.

CLI de CodexOpt:

```bash
uv run codexopt improve              # Preview seguro
uv run codexopt improve --live       # Optimizacion completa con Codex
uv run codexopt improve --live --apply  # Aplicar cambios validados
uv run codexopt report               # Revisar resultados
```

Notas de integracion: los splits train/validation se minan automaticamente del
historial git, issues y descripciones de skills; soporte de trayectorias JSONL
de Codex; multiples senales de recompensa (verificador + juez LLM); ficheros de
evidencia (`tasks.md` o JSON) refuerzan la senal. Mapeo SkillOpt -> CodexOpt:
skill artifact = `SKILL.md`/`AGENTS.md`; rollout = `codex exec` o verificador;
edicion acotada = budget de ediciones; validation gate = rendimiento held-out.

## 13. Comparacion con metodos previos

SkillOpt se posiciona frente a:

- **Skills humanas**: estaticas, no mejoran bajo feedback.
- **One-shot LLM**: generadas de una vez, sin loop de validacion.
- **Trace2Skill**: deriva skills de trazas.
- **TextGrad / GEPA**: optimizacion de prompts/texto via "gradientes textuales";
  SkillOpt anade learning rate acotado + validation gate estricto + buffer de
  rechazos, lo que lo hace mas estable y reproducible.
- **EvoSkill**: evolucion de skills.

Diferencial: es el primero que combina **edicion acotada (add/delete/replace) +
textual learning rate + seleccion held-out gateada + buffer de ediciones
rechazadas + slow/meta update por epoch** de forma sistematica y controlable.

## 14. Relacion con Self-Harness (`Harness.md`)

`docs/Harness.md` contiene el paper **Self-Harness: Harnesses That Improve
Themselves** (Zhang et al., Shanghai AI Lab). Es el primo cercano de SkillOpt;
conviene contrastarlos porque atacan el mismo problema desde angulos distintos.

| Dimension | SkillOpt | Self-Harness |
| --- | --- | --- |
| Objeto que se optimiza | El **documento de skill** (texto en lenguaje natural) | El **harness completo** (prompts, tools, memoria, politicas, codigo de config) |
| Quien propone las ediciones | Un **modelo optimizador separado** (puede ser mas fuerte) | El **mismo modelo fijo** en rol de proposer (sin agente externo mas fuerte) |
| Granularidad de la edicion | add/delete/replace sobre un `skill.md` | Ediciones acotadas a superficies declaradas del harness (instruction, tools, verification...) |
| Control de magnitud | Textual learning rate (lr=4) | Edicion minima por rama, diversidad entre ramas (K propuestas paralelas) |
| Criterio de aceptacion | Mejora estricta del score held-out | No-regresion: mejora en >=1 split sin degradar el otro |
| Memoria de fallos | Rejected-edit buffer + meta-skill | Log de propuestas rechazadas (sin cambiar el harness activo) |
| Evidencia de entrada | Rollouts puntuados, mini-batches | Weakness Mining: clustering de trazas fallidas por firma de fallo verificada |
| Benchmark principal | 6 (SearchQA, SpreadsheetBench, OfficeQA, DocVQA, LiveMath, ALFWorld) | Terminal-Bench-2.0 (64 tareas) |
| Modelos | GPT-5.x, Qwen3.x, MiniMax... (7) | MiniMax M2.5, Qwen3.5-35B-A3B, GLM-5 |
| Artefacto | `best_skill.md` portable (300-2.000 tokens) | Fichero de definicion del harness (codigo DeepAgent) |

Ambos comparten la tesis de fondo: **no hace falta tocar los pesos; basta con
optimizar, de forma testeable y reversible, el estado externo (skill o harness)
que gobierna el comportamiento del agente**, validando contra un held-out gate.
SkillOpt produce un artefacto de texto transferible entre modelos/harnesses;
Self-Harness produce un harness especifico por modelo a partir de sus propios
modos de fallo.

## 15. Relevancia para atenea

`atenea` es un harness de agente (Wails + Go) con sistema de skills incipiente
(`internal/skill/builtin`, `.claude/skills/`) y un tab de terminal con pty real.
Lecturas accionables de SkillOpt para este proyecto:

1. **Las skills como estado entrenable, no como notas estaticas.** El patron
   `best_skill.md` compacto (<2k tokens) y auditable encaja con un fichero de
   skill por dominio que el harness pueda versionar y mejorar.
2. **Validation gate held-out como invariante.** Cualquier auto-mejora de skills
   en atenea deberia aceptar un cambio solo si mejora estrictamente sobre un set
   reservado: esto es el equivalente de skills al ciclo TDD-con-evidencia del
   repo (RED/GREEN/TRIANGULATE con un gate de regresion).
3. **Rollout -> reflect -> gate** es un loop implementable encima del runner
   existente: ejecutar tareas, recoger trazas, proponer ediciones acotadas a la
   skill, validar. El `EventBus`/runner de atenea ya produce trazas explotables.
4. **SkillOpt-Sleep** es el patron mas directamente aplicable: una pasada offline
   que revisa sesiones pasadas y consolida skills validadas entre sesiones, sin
   coste en inferencia de despliegue.
5. **Transferibilidad de skills entre harnesses** (Codex <-> Claude Code) sugiere
   que un `best_skill.md` aprendido en otro entorno podria servir de semilla en
   atenea con beneficio inmediato.

Esto enlaza con el roadmap de `[[subagent-harness-research]]` y
`[[agent-next-additions-roadmap]]`: un optimizador de skills al estilo SkillOpt
seria un Tier futuro natural una vez la persistencia de snapshots y el compactor
de contexto esten en su sitio.

## 16. Limitaciones y cautelas

- **Coste de optimizacion**: rollouts repetidos + optimizador frontera; hasta
  ~210M tokens en benchmarks academicos (mucho menos en uso real).
- **Transferibilidad inconsistente**: funciona pero la preservacion entre
  variantes de modelo es desigual; ganancias cross-benchmark modestas.
- **Dependencia de buenos verificadores**: el gate held-out solo es tan bueno
  como la senal de puntuacion (verificador / juez LLM). Tareas sin verificador
  fiable debilitan el loop.
- **Riesgo de overfitting al benchmark**: como en Self-Harness, las ediciones
  aceptadas pueden reflejar patrones especificos del benchmark.
- **Fiabilidad de las fuentes**: fechas 2026 y modelos GPT-5.5/Qwen3.5 estan
  adelantados respecto al conocimiento previo; las cifras de prensa y de CodexOpt
  (producto de terceros) deben tomarse con cautela frente al paper original.

## 17. Fuentes

- [SkillOpt — project page (microsoft.github.io)](https://microsoft.github.io/SkillOpt/)
- [GitHub — microsoft/SkillOpt](https://github.com/microsoft/SkillOpt)
- [arXiv:2605.23904 — SkillOpt: Executive Strategy for Self-Evolving Agent Skills](https://arxiv.org/abs/2605.23904)
- [Microsoft Research — publication page](https://www.microsoft.com/en-us/research/publication/skillopt-executive-strategy-for-self-evolving-agent-skills/)
- [Hugging Face — paper page](https://huggingface.co/papers/2605.23904)
- [AI Papers Academy — SkillOpt: 2x Accuracy Without Touching the Model](https://aipapersacademy.com/skillopt/)
- [VentureBeat — Microsoft's open-source SkillOpt](https://venturebeat.com/orchestration/microsofts-open-source-skillopt-automatically-upgrades-ai-agent-skills-without-touching-model-weights)
- [Superagentic AI — CodexOpt brings SkillOpt to Codex](https://shashikantjagtap.net/codexopt-brings-microsoft-skillopt-to-codex-optimizing-agent-skills-with-execution-feedback/)
- [Medium — How Microsoft SkillOpt Optimizes LLM Agents by Rewriting skills.md](https://medium.com/@tort_mario/how-microsoft-skillopt-optimizes-llm-agents-by-rewriting-skills-md-25-gain-6d170a07a380)
- Documento relacionado en este repo: `docs/Harness.md` (paper *Self-Harness*).
