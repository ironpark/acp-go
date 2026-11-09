package acp2

import (
	"iter"
	"strings"

	schema "github.com/ironpark/acp-go/schema/v2"
)

// TextBlock wraps text as a content block, the usual shape of a prompt or a
// message chunk.
func TextBlock(text string) ContentBlock {
	return schema.NewContentBlock(schema.ContentBlockText{Text: text})
}

// TextOf returns the text of a text content block, or false for any other kind.
func TextOf(block ContentBlock) (string, bool) {
	text, ok := block.As[schema.ContentBlockText]()
	return text.Text, ok
}

// Texts yields the text of each text block in blocks, skipping other kinds.
func Texts(blocks []ContentBlock) iter.Seq[string] {
	return func(yield func(string) bool) {
		for _, block := range blocks {
			if text, ok := TextOf(block); ok && !yield(text) {
				return
			}
		}
	}
}

// JoinTexts concatenates the text blocks in blocks, skipping other kinds,
// such as a prompt's text:
//
//	prompt := acp2.JoinTexts(params.Prompt)
func JoinTexts(blocks []ContentBlock) string {
	var b strings.Builder
	for text := range Texts(blocks) {
		b.WriteString(text)
	}
	return b.String()
}

// ToolText wraps text as tool call output.
func ToolText(text string) ToolCallContent {
	return schema.NewToolCallContent(schema.ToolCallContentContent{Content: TextBlock(text)})
}

// ToolDiff is tool call content showing file changes, built with
// [NewDiffChange] from [DiffChangeModify], [DiffChangeAdd] and the other
// change kinds.
func ToolDiff(changes ...DiffChange) ToolCallContent {
	return schema.NewToolCallContent(schema.ToolCallContentDiff{Changes: changes})
}

// ToolTerminal embeds a terminal in a tool call, so the client shows its
// output as it runs.
func ToolTerminal(id TerminalID) ToolCallContent {
	return schema.NewToolCallContent(schema.ToolCallContentTerminal{TerminalID: id})
}

// SlashCommand is an available command the client offers as /name, the
// common case of [AvailableCommand]; set its Input with
// [AvailableCommandCommand] and [NewAvailableCommand] when it takes one.
func SlashCommand(name, description string) AvailableCommand {
	return schema.NewAvailableCommand(schema.AvailableCommandCommand{Name: name, Description: description})
}

// CommandOf returns the command an available command holds, or false for a
// custom command type.
func CommandOf(command AvailableCommand) (AvailableCommandCommand, bool) {
	return command.As[schema.AvailableCommandCommand]()
}
