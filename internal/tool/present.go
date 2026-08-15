package tool

import (
	"encoding/json"
	"fmt"
	"path"
	"strconv"
	"strings"
)

// This file holds the Presenter implementation of every tool atenea ships. They
// live together, unlike Effects and GrantRule which sit beside the tool they
// describe, because they are one vocabulary: the labels have to be consistent with
// each other, and reading them apart is how a transcript ends up saying "Read" for
// one tool and "bash" for the next.
//
// Each one is a pure projection of the call and its result, so a malformed input
// yields a bare label rather than an error. That is deliberate: a presentation
// is drawn while the model is still streaming, and half a JSON object is the
// normal case, not a failure.

// Present: a read is about one file, and its output is the file itself — the
// model's business, not something to repeat in the transcript. The subject is the
// file's name rather than its path, because the path is long and the name is what
// distinguishes one read from the next.
func (*ReadTool) Present(call Call, _ Result) Presentation {
	var in struct {
		Path string `json:"path"`
	}
	json.Unmarshal(call.Input, &in)
	return Presentation{
		Label:   "Read",
		Running: "Reading",
		Subject: fileName(stripSelector(in.Path)),
		Detail:  DetailHidden,
	}
}

// Present: what a write amounts to is the path plus everything that will land in
// it, which is exactly what the user has to see before approving one. Once it has
// settled, the diff is the whole of the new file, with nothing to compare against.
func (*WriteTool) Present(call Call, result Result) Presentation {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	json.Unmarshal(call.Input, &in)
	p := Presentation{Label: "Write", Subject: in.Path, Detail: DetailHidden}
	if in.Path != "" {
		p.Body = strings.TrimRight(in.Path+"\n"+in.Content, "\n")
	}
	if result.Diff != "" {
		p.Kind = FileCreation
	}
	return p
}

// Present: the hashline patch IS the faithful pre-execution account of the change
// — its [path#HASH] header names the file and its hunks carry the new lines — so
// it serves as the body unmodified. The subject comes out of that same header,
// since the file being edited is not a field of the input.
func (et *EditTool) Present(call Call, result Result) Presentation {
	paths, _ := et.targetPaths(call.Input, false)
	subject := ""
	if len(paths) == 1 {
		subject = fileName(paths[0])
	} else if len(paths) > 1 {
		subject = strconv.Itoa(len(paths)) + " files"
	}
	body := field(call.Input, "input")
	if body == "" {
		body = string(call.Input)
	}
	p := Presentation{Label: "Edit", Subject: subject, Body: body, Detail: DetailHidden}
	if result.Diff != "" {
		p.Kind = FileChange
	}
	return p
}

// Present: the command is both what identifies the call and what the user
// authorizes, and its output is the reason the call was made, so it stays visible.
func (*BashTool) Present(call Call, _ Result) Presentation {
	command := bashCommand(call.Input)
	return Presentation{Label: "Bash", Subject: command, Body: command, Detail: DetailOnDemand}
}

// Present: the pattern distinguishes one search from the next. Once the search
// settles, its result count says enough for the transcript; the paths and matches
// remain in the result for the model.
func (*GlobTool) Present(call Call, result Result) Presentation {
	return Presentation{
		Label:   "Glob",
		Subject: searchSubject(field(call.Input, "pattern"), globResultCount(result.Output), false),
		Detail:  DetailHidden,
	}
}

func (*GrepTool) Present(call Call, result Result) Presentation {
	count, moreAvailable := grepResultSummary(result.Output)
	return Presentation{
		Label:   "Grep",
		Subject: searchSubject(field(call.Input, "pattern"), count, moreAvailable),
		Detail:  DetailHidden,
	}
}

// Present: the skill's name is the subject, and its body — the whole SKILL.md that
// comes back as output — is for the model. Repeating it in the transcript would
// bury the conversation under instructions the reader did not ask for.
func (*SkillTool) Present(call Call, _ Result) Presentation {
	return Presentation{Label: "Skill", Subject: field(call.Input, "name"), Detail: DetailHidden}
}

// Present: the plan's title names it. The plan itself is shown to the user by the
// plan-mode approval flow, not by the call's line in the transcript.
func (*PresentPlanTool) Present(call Call, _ Result) Presentation {
	return Presentation{Label: "Plan", Subject: field(call.Input, "title"), Detail: DetailHidden}
}

// Present: the checklist is drawn from the call's input by whatever surface owns
// todos, so the line itself only has to say how many there are. The output is an
// acknowledgement with no information in it.
func (TodoWriteTool) Present(call Call, _ Result) Presentation {
	var in struct {
		Todos []struct {
			Status string `json:"status"`
		} `json:"todos"`
	}
	json.Unmarshal(call.Input, &in)
	subject := ""
	if len(in.Todos) > 0 {
		done := 0
		for _, todo := range in.Todos {
			if todo.Status == "completed" {
				done++
			}
		}
		subject = strconv.Itoa(done) + "/" + strconv.Itoa(len(in.Todos))
	}
	return Presentation{Label: "Todo", Subject: subject, Detail: DetailHidden}
}

// Present: the URL is what identifies the fetch and what the user is asked to
// authorize. The output is the answer distilled from the page, which is the point
// of the call.
func (*WebFetchTool) Present(call Call, _ Result) Presentation {
	url := field(call.Input, "url")
	return Presentation{Label: "WebFetch", Subject: url, Body: url}
}

func searchSubject(pattern string, count int, truncated bool) string {
	if pattern == "" || count < 0 {
		return pattern
	}
	if count == 0 {
		return pattern + " (no results)"
	}
	if truncated || count > 100 {
		return pattern + " (100+)"
	}
	return pattern + " (" + strconv.Itoa(count) + ")"
}

func grepResultSummary(output string) (count int, moreAvailable bool) {
	output = strings.TrimSpace(output)
	if output == "" {
		return -1, false
	}
	if output == "No files found" {
		return 0, false
	}
	if _, err := fmt.Sscanf(output, "Found %d matches", &count); err != nil {
		return -1, false
	}
	return count, strings.Contains(output, "(more matches available)")
}

func globResultCount(output string) int {
	output = strings.TrimSpace(output)
	if output == "" {
		return -1
	}
	if output == "No files found" {
		return 0
	}
	count := 0
	for line := range strings.Lines(output) {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "[") {
			count++
		}
	}
	return count
}

// field pulls one string field out of a tool input, tolerating a malformed or
// half-streamed object: the presentation of an incomplete call is a bare label,
// never an error.
func field(input json.RawMessage, name string) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(input, &fields) != nil {
		return ""
	}
	var value string
	if json.Unmarshal(fields[name], &value) != nil {
		return ""
	}
	return value
}

// stripSelector drops the `:from-to` line selector a read path may carry. It
// splits on the LAST colon and only when what follows reads as a selector, so a
// path is never truncated by a colon that belongs to it.
func stripSelector(readPath string) string {
	index := strings.LastIndex(readPath, ":")
	if index < 0 {
		return readPath
	}
	selector := readPath[index+1:]
	if selector == "" || strings.Trim(selector, "0123456789-") != "" {
		return readPath
	}
	return readPath[:index]
}

// fileName is the last segment of a path, on either separator: the shortest thing
// that still tells two calls apart.
func fileName(filePath string) string {
	if filePath == "" {
		return ""
	}
	return path.Base(strings.ReplaceAll(filePath, "\\", "/"))
}

// patchPath reads the file out of a hashline patch's [path#HASH] header. An
// unparseable patch yields "", and the presentation falls back to the bare label.
func patchPath(patch string) string {
	header, _, _ := strings.Cut(patch, "\n")
	header = strings.TrimSpace(header)
	if !strings.HasPrefix(header, "[") {
		return ""
	}
	inside, _, closed := strings.Cut(header[1:], "]")
	if !closed {
		return ""
	}
	filePath, _, _ := strings.Cut(inside, "#")
	return filePath
}
