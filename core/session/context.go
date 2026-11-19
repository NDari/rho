package session

import (
	"fmt"

	"github.com/NDari/rho/core/provider"
)

// Synthetic message markers (provider.Meta.Synthetic).
const (
	SyntheticCompaction = "compaction"
	SyntheticAsideNote  = "aside_note"
)

// Context is the conversation as it will be sent to the model.
type Context struct {
	Messages []provider.Message
	// EntryIDs[i] is the entry that produced Messages[i] ("" for synthetic).
	EntryIDs []string
	// Compaction is the active compaction entry, if any.
	Compaction *Entry
	// Aside reports that the current position is inside a side conversation.
	Aside bool
	// MainLeaf is the main-branch entry the aside started from.
	MainLeaf string
	// Provider and Model are from the last model_change on the path.
	Provider string
	Model    string
}

// BuildContext assembles the context for the current leaf.
func (s *Session) BuildContext() Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	return buildContext(s.pathTo(s.leaf))
}

// ContextAt assembles the context for an arbitrary entry.
func (s *Session) ContextAt(id string) Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	return buildContext(s.pathTo(id))
}

func buildContext(path []Entry) Context {
	var c Context
	start := 0
	for i := len(path) - 1; i >= 0; i-- {
		if path[i].Type == TypeCompaction {
			e := path[i]
			c.Compaction = &e
			start = i + 1
			for j := 0; j < i; j++ {
				if path[j].ID == e.FirstKept {
					start = j
					break
				}
			}
			break
		}
	}
	for i := len(path) - 1; i >= 0; i-- {
		if path[i].Type == TypeAsideStart {
			c.Aside = true
			c.MainLeaf = path[i].Parent
			break
		}
	}
	for _, e := range path {
		if e.Type == TypeModelChange {
			c.Provider, c.Model = e.Provider, e.Model
		}
	}
	if c.Compaction != nil {
		c.Messages = append(c.Messages, SummaryMessage(c.Compaction.Summary))
		c.EntryIDs = append(c.EntryIDs, "")
	}
	for _, e := range path[start:] {
		switch e.Type {
		case TypeMessage:
			if e.Message != nil {
				c.Messages = append(c.Messages, *e.Message)
				c.EntryIDs = append(c.EntryIDs, e.ID)
			}
		case TypeAsideSummary:
			c.Messages = append(c.Messages, provider.Message{
				Role:    provider.RoleUser,
				Content: []provider.Block{provider.Text(fmt.Sprintf("[Note carried back from a side conversation]\n%s", e.Note))},
				Meta:    provider.Meta{Synthetic: SyntheticAsideNote},
			})
			c.EntryIDs = append(c.EntryIDs, e.ID)
		}
	}
	return c
}

// SummaryMessage wraps a compaction summary as a user message.
func SummaryMessage(summary string) provider.Message {
	return provider.Message{
		Role: provider.RoleUser,
		Content: []provider.Block{provider.Text(
			"The earlier part of this conversation was compacted into the following summary:\n\n<summary>\n" +
				summary + "\n</summary>")},
		Meta: provider.Meta{Synthetic: SyntheticCompaction},
	}
}
