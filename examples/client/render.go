package main

import (
	"cmp"
	"fmt"
	"strings"

	"github.com/ironpark/acp-go/acp1"
)

// render prints one update with a type switch over the SessionUpdate union.
func (c *exampleClient) render(update acp1.SessionUpdate) {
	switch update := update.Variant().(type) {
	case acp1.SessionUpdateAgentMessageChunk:
		if text, ok := acp1.TextOf(update.Content); ok {
			fmt.Print(text)
		} else {
			fmt.Print("[non-text content]")
		}
	case acp1.SessionUpdateAgentThoughtChunk:
		if text, ok := acp1.TextOf(update.Content); ok {
			fmt.Printf("\n💭 %s", text)
		}
	case acp1.SessionUpdateToolCall:
		c.setToolTitle(update.ToolCallID, update.Title)
		fmt.Printf("\n🔧 %s", update.Title)
		if update.Status != nil {
			fmt.Printf(" (%s)", *update.Status)
		}
		fmt.Println()
		c.renderContent(update.Content)
	case acp1.SessionUpdateToolCallUpdate:
		// Updates name the tool call by its id; show the title it started with.
		title := cmp.Or(update.GetTitle(), c.toolTitle(update.ToolCallID))
		fmt.Printf("🔧 %s", title)
		if update.Status != nil {
			fmt.Printf(": %s", *update.Status)
		}
		fmt.Println()
		c.renderContent(update.Content)
	case acp1.SessionUpdatePlan:
		fmt.Println("\n📋 Plan")
		for _, entry := range update.Entries {
			fmt.Printf("   %s %s\n", planMarks[entry.Status], entry.Content)
		}
	case acp1.SessionUpdateCurrentModeUpdate:
		fmt.Printf("\n🎛  mode: %s\n", update.CurrentModeID)
	default:
		// Includes acp1.SessionUpdateUnknown: updates newer than this SDK
		// are safe to ignore.
	}
}

var planMarks = map[acp1.PlanEntryStatus]string{
	acp1.PlanEntryStatusPending:    "[ ]",
	acp1.PlanEntryStatusInProgress: "[>]",
	acp1.PlanEntryStatusCompleted:  "[x]",
}

// renderContent prints a tool call's output: text, a file diff, or the
// output of a terminal the agent ran a command in.
func (c *exampleClient) renderContent(content []acp1.ToolCallContent) {
	for _, item := range content {
		switch item := item.Variant().(type) {
		case acp1.ToolCallContentContent:
			if text, ok := acp1.TextOf(item.Content); ok {
				fmt.Print(indent(text, "   "))
			}
		case acp1.ToolCallContentDiff:
			fmt.Printf("   %s\n", item.Path)
			if item.OldText != nil {
				fmt.Print(indent(*item.OldText, "   - "))
			}
			fmt.Print(indent(item.NewText, "   + "))
		case acp1.ToolCallContentTerminal:
			if t, err := c.terminals.get(item.TerminalID); err == nil {
				output, _, _ := t.snapshot()
				fmt.Print(indent(output, "   $ "))
			}
		}
	}
}

// indent prefixes each line of text.
func indent(text, prefix string) string {
	var b strings.Builder
	for line := range strings.Lines(text) {
		b.WriteString(prefix + line)
	}
	if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
		b.WriteByte('\n')
	}
	return b.String()
}
