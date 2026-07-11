package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
)

const composerMaxLines = 5

type composerInput struct {
	textarea.Model
}

func newComposerInput() composerInput {
	input := textarea.New()
	input.Prompt = inputPrompt
	input.ShowLineNumbers = false
	input.EndOfBufferCharacter = ' '
	input.MaxHeight = composerMaxLines
	input.SetHeight(1)
	input.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("ctrl+j"))
	input.FocusedStyle.Prompt = accentStyle
	input.BlurredStyle.Prompt = accentStyle
	input.Focus()
	return composerInput{Model: input}
}

func (input *composerInput) SetValue(value string) {
	input.Model.SetValue(value)
	input.resize()
}

func (input composerInput) Position() int {
	lines := strings.Split(input.Value(), "\n")
	position := 0
	for index := 0; index < input.Line() && index < len(lines); index++ {
		position += len([]rune(lines[index])) + 1
	}
	return position + input.LineInfo().StartColumn + input.LineInfo().ColumnOffset
}

func (input *composerInput) SetCursor(position int) {
	position = max(position, 0)
	lines := strings.Split(input.Value(), "\n")
	row := 0
	for row < len(lines)-1 {
		lineLength := len([]rune(lines[row]))
		if position <= lineLength {
			break
		}
		position -= lineLength + 1
		row++
	}
	for input.Line() > row {
		input.CursorUp()
	}
	for input.Line() < row {
		input.CursorDown()
	}
	input.Model.SetCursor(position)
}

func (input *composerInput) CursorEnd() {
	for input.Line() < input.LineCount()-1 {
		input.CursorDown()
	}
	input.Model.CursorEnd()
}

func (input composerInput) Update(msg tea.Msg) (composerInput, tea.Cmd) {
	var cmd tea.Cmd
	input.Model, cmd = input.Model.Update(msg)
	input.resize()
	return input, cmd
}

func (input *composerInput) resize() {
	input.SetHeight(min(max(input.LineCount(), 1), composerMaxLines))
}
