// Package skill descubre y formatea las skills del workspace para el agente.
//
// Una skill es un directorio con un SKILL.md: frontmatter (name, description) mas
// un cuerpo Markdown con instrucciones y recursos. El agente expone disclosure
// progresivo en dos niveles, como opencode: solo los metadatos (name +
// description) viajan en el system prompt (ver Format), y el cuerpo completo se
// carga bajo demanda cuando el modelo invoca la tool skill (ver internal/tool).
package skill
