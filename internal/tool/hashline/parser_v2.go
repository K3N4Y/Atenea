package hashline

// Preview applies a parsed patch section to text without filesystem, snapshot,
// or live clipboard mutation. The supplied clipboard is cloned.
func Preview(text string, section Section, clipboard *Clipboard) (ApplyResult, error) {
	return ApplyEditsWithClipboard(SplitLines(text), section.Edits, clipboard.Clone())
}
