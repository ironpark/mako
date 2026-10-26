package protocol

const (
	TypeSearch       Type = "search"
	TypeSearchResult Type = "search_result"
)

// Search asks a node for a project's files, or the lines in them that hold
// Query, as written: the case is matched only when Query has a capital.
// Both leave out what the project's .gitignore does.
type Search struct {
	NodeID string `json:"node_id,omitempty"`
	Root   string `json:"root"` // absolute
	Query  string `json:"query,omitempty"`
	Files  bool   `json:"files,omitempty"` // list the files rather than search them
}

// SearchMatch is a line holding the query: its file, from Root, its number
// and the query's column in it, from 1, and the line itself.
type SearchMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Col  int    `json:"col"`
	Text string `json:"text"`
}

// SearchResult is the reply to Search: the files, or the matches, up to a
// limit; Truncated says there were more.
type SearchResult struct {
	Files     []string      `json:"files,omitempty"`
	Matches   []SearchMatch `json:"matches,omitempty"`
	Truncated bool          `json:"truncated,omitempty"`
}
