# Identidad Visual y UX (Atenea)

## 1. Concepto Principal
La aplicación busca romper la barrera entre perfiles técnicos y creativos mediante el uso de la Inteligencia Artificial. La interfaz debe transmitir accesibilidad, fluidez y simplicidad, haciendo que cualquier usuario se sienta empoderado y no intimidado.

## 2. Principios de Diseño (UX/UI)
- **Minimalismo ("Clean UI"):** La pantalla principal debe estar libre de ruido visual. El usuario no debe sentirse abrumado al abrir la aplicación.
- **Zero-Friction (Chat First):** Siempre que el usuario entra a la aplicación, debe encontrarse con un chat nuevo abierto, listo para interactuar al instante.
- **Formas Suaves y Orgánicas:** Uso intensivo de bordes muy redondeados para evitar que el entorno se sienta agresivo:
  - **Elementos generales (contenedores, inputs, etc.):** usar un `border-radius` de **24px**.
  - **Botones:** usar un redondeado total (**full / estilo píldora**).
- **Estructura Plana (Sin Cards):** Evitar el diseño tradicional basado en tarjetas (cards). La separación de elementos debe lograrse mediante el uso de espacios en blanco (padding/margin) y contrastes muy sutiles, creando una vista más unificada y continua.

## 3. Paleta de Colores
- **Color Base / Fondo:** **Blanco Papel** (un tono ligeramente cálido y cómodo para la vista prolongada, ej. `#fef9ed` o `#F9F9F8`). Evitar el blanco puro `#FFFFFF` brillante.
- **Color de Acento:** **Naranja**. Representa creatividad y energía. Debe utilizarse de forma **muy moderada y estratégica** (solo para el botón de enviar, estados activos o indicadores sutiles) para no cansar la vista ni quitar protagonismo al contenido.

## 4. Layout (Estructura de la Pantalla)
- **Área principal:** El chat, ocupando la mayor parte de la pantalla, centrado y limpio.
- **Barra Lateral Izquierda (Sidebar):** 
  - Contendrá el historial de chats o navegaciones secundarias.
  - **Requisito funcional:** Debe tener estado persistente (si el usuario la ocultó, debe seguir oculta al volver a abrir la app).

## 5. Arquitectura de Desarrollo (Vue)
- La interfaz debe construirse bajo una estricta **arquitectura de componentes funcionales e independientes**.
- Cada parte de la UI (sidebar, input de mensaje, burbuja de chat) debe ser un archivo separado para hacer que el código sea tan limpio y fácil de navegar como la interfaz visual.

## 6. Tipografía
- **Fuente Universal (Chat y Código):** **Red Hat Mono**. Al utilizar una tipografía monoespaciada para toda la interfaz (tanto para los mensajes coloquiales como para los bloques de código), se unifica el contexto técnico con el diseño limpio. Esto le aporta mucha personalidad a la herramienta y mantiene la consistencia visual.

## 7. Iconografía
- **Librería de Iconos:** **Phosphor Icons**. 
- **Estilo:** Se debe priorizar el estilo *Regular* o *Light* (trazos finos y consistentes). Sus terminaciones redondeadas y legibilidad encajan perfectamente con el enfoque de bordes suaves y la tipografía monoespaciada, manteniendo el peso visual de la interfaz al mínimo.

## 8. Anatomía del Mensaje del Chat
La conversación debe sentirse como un lienzo continuo y plano (ver "Estructura Plana"), diferenciando los elementos solo cuando aporta claridad:
- **Mensajes del usuario:** llevan un **fondo (background) sutil** que los distingue del lienzo, pero **sin borde**. El contenedor respeta el redondeado general (24px).
- **Respuestas de la IA (render de Markdown):** se renderizan **directamente sobre el fondo Blanco Papel**, sin fondo ni contenedor propio, de modo que el contenido fluya integrado en la vista.
- **Herramientas `edit` y `diff`:** estos bloques **sí llevan su propio fondo (background)** para separar visualmente el código y los cambios del flujo conversacional.

## 9. Streaming de Pensamiento (Thinking Process)
Para reflejar el proceso de razonamiento de la IA sin saturar la interfaz, se debe implementar el siguiente comportamiento:
- **Durante la generación (Activo):** Solo se mostrarán las **últimas 4 líneas** del pensamiento en curso de la IA.
- **Al finalizar (Colapsado):** El bloque completo se colapsará automáticamente en una sola línea que indique "Thought" acompañado del tiempo total que tardó.
- **Expansión opcional:** Una vez que el pensamiento haya terminado, el usuario debe poder **expandir completamente el bloque** para ver el contenido completo si lo desea.
- **Formato del cronómetro (Tiempo de proceso):**
  - Menos de 200ms: `"briefly"` (brevemente).
  - Entre 200ms y 999ms: Mostrar los milisegundos (ej. `"450ms"`).
  - A partir de 1 segundo: Formato progresivo dinámico (ej. `"3m 5s"`, `"1h 15m 13s"`).


## 10. Tool Read
Al ejecutar la herramienta `read`, la interfaz debe reflejar su estado mediante una etiqueta acompañada del nombre del archivo:
- **Durante la lectura (Activo):** mostrar `"Reading"` junto al nombre del archivo.
- **Al finalizar:** mostrar `"Read"` junto al nombre del archivo.
- **Nombre del archivo:** mostrar únicamente el nombre del archivo, nunca la ruta completa.

## 11. Voz y Microcopy
Para que el usuario sienta que el agente está trabajando de verdad, la interfaz debe comunicar progreso, intención y control en cada etapa de la interacción. La voz debe ser clara, cálida y ligeramente técnica, sin sonar fría ni exagerada.

### Principios
- **Mostrar progreso real:** cada acción del agente debe sentirse como un avance concreto, no como un estado genérico de "cargando".
- **Dar sensación de control:** el usuario debe entender qué está pasando y qué puede esperar, sin sentirse a merced del sistema.
- **Hablar con tranquilidad:** el lenguaje debe ser breve, seguro y humano, evitando frases vacías o demasiado verbosas.
- **Reforzar la agencia del usuario:** el sistema debe mostrar que el usuario sigue al mando, incluso cuando el agente está ejecutando tareas.

### Comportamiento esperado
- **Durante la acción:** mostrar microcopy orientada a la actividad en curso, por ejemplo: `"Working"`, `"Checking context"`, `"Preparing response"`, `"Reviewing changes"`.
- **Cuando hay avance visible:** comunicarlo con frases cortas que sugieren movimiento y progreso, por ejemplo: `"Found relevant context"`, `"Drafting answer"`, `"Almost done"`.
- **Cuando el agente está esperando o procesando:** usar mensajes que transmitan continuidad y ritmo, sin generar ansiedad, por ejemplo: `"Still working"`, `"Continuing"`.
- **Cuando el usuario necesita orientación:** ofrecer mensajes simples que transmitan control, por ejemplo: `"You can keep going"`, `"You can stop anytime"`, `"This step is in progress"`.

### Regla de tono
- La interfaz debe evitar microcopy pasiva o vacía como `"loading"` cuando exista una oportunidad para comunicar algo más significativo.
- El copy debe ser útil, concreto y orientado al usuario, no meramente decorativo.
- Cada mensaje de estado debe ayudar a responder una pregunta básica: "¿qué está haciendo el agente y qué pasa ahora?"