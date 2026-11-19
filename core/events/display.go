package events

// Display is structured data that tells frontends how to present a tool
// result. It is never sent to the model.
type Display struct {
	// Kind is "diff", "text" or "image".
	Kind string `json:"kind"`
	Path string `json:"path,omitempty"`
	// Language is a syntax highlighting hint (usually from the extension).
	Language string `json:"language,omitempty"`
	Diff     *Diff  `json:"diff,omitempty"`
	Text     string `json:"text,omitempty"`
	// ExitCode is set for commands.
	ExitCode *int `json:"exit_code,omitempty"`
	// Truncated reports that Text or the model result was shortened.
	Truncated bool `json:"truncated,omitempty"`
	// FullOutputPath points to the complete output when truncated.
	FullOutputPath string `json:"full_output_path,omitempty"`
	// Summary is a one-line description (e.g. "120 lines").
	Summary string `json:"summary,omitempty"`
}

// Diff is a unified diff split into hunks.
type Diff struct {
	NewFile bool   `json:"new_file,omitempty"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Hunks   []Hunk `json:"hunks"`
}

// Hunk is a contiguous region of a diff.
type Hunk struct {
	OldStart int        `json:"old_start"`
	NewStart int        `json:"new_start"`
	Lines    []DiffLine `json:"lines"`
}

// DiffLine kinds.
const (
	LineContext = ' '
	LineAdded   = '+'
	LineRemoved = '-'
)

// DiffLine is one line of a hunk.
type DiffLine struct {
	Kind rune   `json:"kind"`
	Text string `json:"text"`
}
