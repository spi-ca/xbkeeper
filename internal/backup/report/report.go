// Package report defines the backup operation results shared by the engine and
// its presentation layer. JSON field names are part of the command contract.
package report

// Backup identifies the newly completed backup after a successful operation.
type Backup struct {
	Name string `json:"name"`
}

// Status is the structurally validated inventory, ordered by preparation time.
type Status struct {
	Backups          []string `json:"backups"`
	Incomplete       []string `json:"incomplete"`
	PendingDeletions []string `json:"pending_deletions"`
	LastSuccess      string   `json:"last_success,omitempty"`
}

// Verification is one content audit outcome, including partial hash counts.
type Verification struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Files  int    `json:"files"` // successfully hashed files; zero if none checked
	Reason string `json:"reason,omitempty"`
}

// Verify retains audit outcomes completed before an error or cancellation.
type Verify struct {
	Backups []Verification `json:"backups"`
}
