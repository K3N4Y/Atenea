// Package repair repairs tool inputs that arrive from the model in
// almost-valid shapes, following the validate-then-repair policy: the input
// is first validated against the tool's schema and known repairs are applied
// only when it does not conform. An already-valid input passes through byte
// for byte (no re-serialization or normalization) and without notes.
//
// Repairs act in two layers. First the tolerant parse of the raw input, for
// malformed JSON: it escapes literal control characters inside strings,
// closes truncated objects, unwraps a root array containing a single object,
// and wraps a bare root string into the only required string property. Then
// the field-by-field rules over the decoded object: they rename known aliases
// to the schema's canonical key, drop null fields, and coerce
// JSON-stringified values to the declared type.
//
// Every repair leaves a note: model-facing text explaining what was repaired
// and how to send the input correctly next time. WithNotes prepends those
// notes to the tool output as <repair_note> lines so the model sees them on
// its next turn and corrects its behavior.
//
// If the input still does not conform to the schema after repairing, Repair
// returns an *InvalidInputError with one Issue per deviation. Its message is
// aimed at the model (fix and retry), never a raw JSON decoder error.
package repair

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Issue is a validation deviation against the schema: which field fails and
// why. Root-level issues use the "(root)" field.
type Issue struct {
	Field   string
	Message string
}

// InvalidInputError is the error Repair returns when the input still does not
// conform to the schema after applying repairs. Its message is aimed at the
// model: it lists the issues and asks it to fix the input and retry.
type InvalidInputError struct {
	Tool   string
	Issues []Issue
}

func (e *InvalidInputError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Invalid input for tool %q. Fix it and retry:", e.Tool)
	for _, issue := range e.Issues {
		fmt.Fprintf(&b, "\n  • %s: %s", issue.Field, issue.Message)
	}
	return b.String()
}

// objectSchema is the slice of the top-level JSON schema this layer needs:
// the map of declared properties and the required properties.
type objectSchema struct {
	Properties map[string]propertySchema `json:"properties"`
	Required   []string                  `json:"required"`
}

// propertySchema is the slice of the JSON schema this layer needs per
// property: only the declared type.
type propertySchema struct {
	Type string `json:"type"`
}

// Repair validates the input against the schema and repairs known deviations.
// An already-valid input is returned byte for byte, without notes. Otherwise
// a tolerant parse of the raw input is applied, then field-by-field repair
// rules; each repair leaves a model-facing note.
func Repair(toolName string, schema, input json.RawMessage) (json.RawMessage, []string, error) {
	var spec objectSchema
	if err := json.Unmarshal(schema, &spec); err != nil {
		return nil, nil, fmt.Errorf("invalid schema for tool %s: %w", toolName, err)
	}

	fields, notes, err := parseFields(toolName, spec, input)
	if err != nil {
		return nil, nil, err
	}

	// Passthrough: no notes means the parse was direct (the raw input was
	// untouched); if it also validates, it is returned intact, byte for byte.
	if len(notes) == 0 && len(validate(spec, fields)) == 0 {
		return input, nil, nil
	}

	notes = append(notes, repairFields(toolName, spec, fields)...)

	// Final validation: if issues remain after repairing, the input is
	// irreparable and the model gets a readable error with every issue.
	if issues := validate(spec, fields); len(issues) > 0 {
		return nil, nil, &InvalidInputError{Tool: toolName, Issues: issues}
	}

	repaired, err := json.Marshal(fields)
	if err != nil {
		return nil, nil, fmt.Errorf("could not re-serialize the repaired input for tool %s: %w", toolName, err)
	}
	return repaired, notes, nil
}

// WithNotes prepends each repair note to the tool output as a
// <repair_note>...</repair_note> line, so the model sees what was repaired
// and corrects its next call. Without notes it returns the output unchanged.
func WithNotes(notes []string, output string) string {
	if len(notes) == 0 {
		return output
	}
	var b strings.Builder
	for _, note := range notes {
		b.WriteString("<repair_note>")
		b.WriteString(note)
		b.WriteString("</repair_note>\n")
	}
	b.WriteString(output)
	return b.String()
}

// parseFields decodes the raw input into a field map applying, in order,
// known format repairs (tolerant parse). No notes means the parse was direct
// and the raw input was left intact; each applied repair leaves its own note.
func parseFields(toolName string, spec objectSchema, input json.RawMessage) (map[string]json.RawMessage, []string, error) {
	// a. Direct parse.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(input, &fields); err == nil {
		return fields, nil, nil
	}

	var notes []string
	base := []byte(input)

	// b. Unescaped control characters inside JSON strings (literal
	// newline/tab inside quotes): escape them and retry.
	if escaped, changed := escapeControlChars(base); changed {
		base = escaped
		notes = append(notes, fmt.Sprintf(
			"The input for tool %s contained unescaped control characters inside JSON strings; they were escaped automatically. Next time escape newlines and tabs as \\n and \\t.",
			toolName,
		))
		if err := json.Unmarshal(base, &fields); err == nil {
			return fields, notes, nil
		}
	}

	// c. Truncated JSON starting with '{': try closing suffixes until it
	// parses as an object.
	trimmed := bytes.TrimSpace(base)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		for _, suffix := range closingSuffixes {
			candidate := []byte(string(trimmed) + suffix)
			if err := json.Unmarshal(candidate, &fields); err != nil {
				continue
			}
			notes = append(notes, fmt.Sprintf(
				"The input for tool %s arrived as truncated JSON; it was closed automatically so it could be parsed. Next time send the complete JSON object.",
				toolName,
			))
			return fields, notes, nil
		}
	}

	// d. Root array with a single object ([{...}]) where the schema expects
	// an object: unwrap to that object.
	var asArray []json.RawMessage
	if err := json.Unmarshal(base, &asArray); err == nil && len(asArray) == 1 {
		if err := json.Unmarshal(asArray[0], &fields); err == nil {
			notes = append(notes, fmt.Sprintf(
				"The input for tool %s arrived as an array containing a single object; that object was used directly. Next time send the JSON object without wrapping it in an array.",
				toolName,
			))
			return fields, notes, nil
		}
	}

	// e. Bare root JSON string ("...") when the schema has exactly one
	// required property of type string: wrap it into an object under that
	// property.
	var asString string
	if err := json.Unmarshal(base, &asString); err == nil && len(spec.Required) == 1 {
		required := spec.Required[0]
		if spec.Properties[required].Type == "string" {
			value, err := json.Marshal(asString)
			if err == nil {
				notes = append(notes, fmt.Sprintf(
					"The input for tool %s arrived as a bare string; it was wrapped as {%q: <value>}. Next time send a JSON object with the %q key.",
					toolName, required, required,
				))
				return map[string]json.RawMessage{required: value}, notes, nil
			}
		}
	}

	return nil, nil, &InvalidInputError{Tool: toolName, Issues: []Issue{{
		Field:   "(root)",
		Message: "the input is not valid JSON and could not be repaired automatically; send a JSON object that conforms to the tool's schema",
	}}}
}

// closingSuffixes are the closers tried, in order, to repair a truncated JSON
// object: a pending string quote and reasonable combinations of braces and
// brackets up to a few nesting levels.
var closingSuffixes = []string{
	`"`, `"}`, `}`,
	`"]}`, `]}`, `"}]}`, `}]}`,
	`"]}]}`, `]}]}`, `"}]}]}`, `}]}]}`,
	`"]}]}]}`, `]}]}]}`, `"}]}]}]}`, `}]}]}]}`,
}

// escapeControlChars escapes the literal control characters (< 0x20) that
// appear inside JSON strings, respecting escape sequences already present. It
// returns the result and whether anything changed.
func escapeControlChars(input []byte) ([]byte, bool) {
	var out bytes.Buffer
	changed := false
	inString := false
	escaped := false
	for _, b := range input {
		if !inString {
			if b == '"' {
				inString = true
			}
			out.WriteByte(b)
			continue
		}
		if escaped {
			out.WriteByte(b)
			escaped = false
			continue
		}
		switch {
		case b == '\\':
			out.WriteByte(b)
			escaped = true
		case b == '"':
			out.WriteByte(b)
			inString = false
		case b < 0x20:
			changed = true
			switch b {
			case '\n':
				out.WriteString(`\n`)
			case '\t':
				out.WriteString(`\t`)
			case '\r':
				out.WriteString(`\r`)
			default:
				fmt.Fprintf(&out, `\u%04x`, b)
			}
		default:
			out.WriteByte(b)
		}
	}
	return out.Bytes(), changed
}

// repairFields applies the field-by-field repair rules over the decoded map,
// in order: rename aliases, drop nulls and repair types. It returns the notes
// for the repairs applied.
func repairFields(toolName string, spec objectSchema, fields map[string]json.RawMessage) []string {
	notes := renameAliases(toolName, spec, fields)
	notes = append(notes, dropNulls(toolName, fields)...)
	notes = append(notes, repairTypes(toolName, spec, fields)...)
	return notes
}

// aliasesByCanonical is the generic table of known aliases by canonical name:
// keys models commonly get wrong when calling tools, grouped under the key
// the schemas actually declare.
var aliasesByCanonical = map[string][]string{
	"path":    {"file_path", "filePath", "filepath", "filename", "file", "absolute_path", "absolutePath", "target_file", "pathname"},
	"pattern": {"query", "regex", "search", "expression"},
	"command": {"cmd", "shell", "script", "commandLine"},
	"content": {"text", "body", "data", "contents", "fileContent"},
	"url":     {"uri", "link", "href"},
	"prompt":  {"question", "instruction"},
	"patch":   {"diff", "edits", "changes"},
	"text":    {"message"},
	"name":    {"skill_name", "skill"},
	"todos":   {"items", "tasks"},
}

// canonicalByAlias is the inverse index of aliasesByCanonical, to resolve in
// a single lookup which canonical key an alias belongs to.
var canonicalByAlias = func() map[string]string {
	index := make(map[string]string)
	for canonical, aliases := range aliasesByCanonical {
		for _, alias := range aliases {
			index[alias] = canonical
		}
	}
	return index
}()

// renameAliases renames input keys that are not in the schema but are known
// aliases of a declared, absent canonical key. It does not rename when the
// canonical key is already present: it never overwrites an existing value.
func renameAliases(toolName string, spec objectSchema, fields map[string]json.RawMessage) []string {
	var notes []string
	for _, name := range sortedKeys(fields) {
		if _, declared := spec.Properties[name]; declared {
			continue
		}
		canonical, isAlias := canonicalByAlias[name]
		if !isAlias {
			continue
		}
		if _, declared := spec.Properties[canonical]; !declared {
			continue
		}
		if _, present := fields[canonical]; present {
			continue
		}
		fields[canonical] = fields[name]
		delete(fields, name)
		notes = append(notes, fmt.Sprintf(
			"Field %q of tool %s does not exist in its schema; it was renamed to %q. Next time use the canonical key %q.",
			name, toolName, canonical, canonical,
		))
	}
	return notes
}

// dropNulls removes fields present with a null value: optional fields are
// omitted, not sent as null.
func dropNulls(toolName string, fields map[string]json.RawMessage) []string {
	var notes []string
	for _, name := range sortedKeys(fields) {
		if jsonType(fields[name]) != "null" {
			continue
		}
		delete(fields, name)
		notes = append(notes, fmt.Sprintf(
			"Field %q of tool %s arrived as null; it was removed. Optional fields are omitted, not sent as null.",
			name, toolName,
		))
	}
	return notes
}

// repairTypes repairs present values whose type does not match the one
// declared in the schema. It only knows how to repair values that arrived as
// strings: if the content parses as the declared type, the JSON-stringified
// value is replaced by the real one; if the schema expects an array and the
// content is not one, it is wrapped in a single-element array.
func repairTypes(toolName string, spec objectSchema, fields map[string]json.RawMessage) []string {
	var notes []string
	for _, name := range sortedKeys(fields) {
		prop, declared := spec.Properties[name]
		if !declared || prop.Type == "string" {
			continue
		}
		value := fields[name]
		if matchesType(prop.Type, value) {
			continue
		}

		var asString string
		if err := json.Unmarshal(value, &asString); err != nil {
			continue
		}

		if candidate := json.RawMessage(asString); json.Valid(candidate) && matchesType(prop.Type, candidate) {
			fields[name] = candidate
			notes = append(notes, fmt.Sprintf(
				"Field %q of tool %s arrived as a string containing a JSON-stringified %s; it was repaired by parsing it into the real value. Next time send the %s as literal JSON, not as a string.",
				name, toolName, prop.Type, prop.Type,
			))
			continue
		}

		if prop.Type == "array" {
			fields[name] = json.RawMessage("[" + string(value) + "]")
			notes = append(notes, fmt.Sprintf(
				"Field %q of tool %s arrived as a string but the schema expects an array; it was wrapped in a single-element array. Next time send a JSON array.",
				name, toolName,
			))
		}
	}
	return notes
}

// validate checks the field map against the schema: every required property
// must be present and every present value must have the correct declared
// type.
func validate(spec objectSchema, fields map[string]json.RawMessage) []Issue {
	var issues []Issue
	for _, required := range spec.Required {
		if _, present := fields[required]; !present {
			issues = append(issues, Issue{Field: required, Message: "this required field is missing"})
		}
	}
	for _, name := range sortedKeys(fields) {
		prop, declared := spec.Properties[name]
		if !declared {
			continue
		}
		if !matchesType(prop.Type, fields[name]) {
			issues = append(issues, Issue{
				Field:   name,
				Message: fmt.Sprintf("expected %s but got %s", prop.Type, jsonType(fields[name])),
			})
		}
	}
	return issues
}

// jsonType returns the JSON type of a raw value: object, array, string,
// boolean, number or null.
func jsonType(value json.RawMessage) string {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 {
		return ""
	}
	switch trimmed[0] {
	case '{':
		return "object"
	case '[':
		return "array"
	case '"':
		return "string"
	case 't', 'f':
		return "boolean"
	case 'n':
		return "null"
	default:
		return "number"
	}
}

// matchesType reports whether the raw value satisfies the type declared in
// the schema. "integer" requires a number without a fractional part; null
// never satisfies any type (Go's decoder would accept it silently, not here).
func matchesType(declared string, value json.RawMessage) bool {
	kind := jsonType(value)
	switch declared {
	case "integer":
		if kind != "number" {
			return false
		}
		var n int64
		return json.Unmarshal(bytes.TrimSpace(value), &n) == nil
	default:
		return kind == declared
	}
}

// sortedKeys returns the map keys in sorted order, so repairs and their notes
// are deterministic.
func sortedKeys(fields map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
