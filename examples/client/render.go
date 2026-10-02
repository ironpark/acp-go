package main

import (
	"cmp"
	"fmt"
	"strings"

	"github.com/ironpark/acp-go/acp1"
)

// render prints one update with a type switch over the SessionUpdate union.
// SessionUpdate calls it, so an update is shown before any request the agent
// sends after it, such as the permission request for a proposed tool call.
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
		c.renderContent(update.ToolCallID, update.Content)
	case acp1.SessionUpdateToolCallUpdate:
		// Updates name the tool call by its id; show the title it started with.
		title := cmp.Or(update.GetTitle(), c.toolTitle(update.ToolCallID))
		fmt.Printf("🔧 %s", title)
		if update.Status != nil {
			fmt.Printf(": %s", *update.Status)
		}
		fmt.Println()
		c.renderContent(update.ToolCallID, update.Content)
		if status := update.GetStatus(); status == acp1.ToolCallStatusCompleted || status == acp1.ToolCallStatusFailed {
			c.renderTerminals(update.ToolCallID)
		}
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

// renderContent prints a tool call's output: text or a file diff. A terminal
// is still running when it is shown, so its output is printed once the tool
// call ends, by renderTerminals.
func (c *exampleClient) renderContent(id acp1.ToolCallID, content []acp1.ToolCallContent) {
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
			c.mu.Lock()
			c.toolTerminals[id] = append(c.toolTerminals[id], item.TerminalID)
			c.mu.Unlock()
		}
	}
}

// renderTerminals prints the output of the terminals shown in a tool call
// that has ended. A released terminal keeps its output for this.
func (c *exampleClient) renderTerminals(id acp1.ToolCallID) {
	c.mu.Lock()
	ids := c.toolTerminals[id]
	delete(c.toolTerminals, id)
	c.mu.Unlock()
	for _, terminalID := range ids {
		if t, err := c.terminals.get(terminalID); err == nil {
			output, _, _ := t.snapshot()
			fmt.Print(indent(output, "   $ "))
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
