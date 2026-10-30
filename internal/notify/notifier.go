package notify

// Capabilities describes what a notification backend can do.
type Capabilities struct {
	Click, Sound, Replace, Retract bool
}

// Notice is one desktop notification. Every value is already rendered from a
// fixed template: no prompt, tool, assistant or error text ever gets here.
type Notice struct {
	Title    string
	Subtitle string
	Body     string
	// Line2 is a second, quieter line under the body. With Markup the backend
	// may style it (italic); otherwise it is plain text.
	Line2  string
	Markup bool
	// Icon is a theme icon name (dialog-question and friends).
	Icon  string
	Sound bool
	// Group names the notice for replace and retract (bosun.<session>.attn).
	Group string
	// ReplaceID is the id of a notice this one replaces; 0 posts a new one.
	ReplaceID uint32
	// Action, when set, is the label of a default action (a click on the notice);
	// backends that cannot offer one ignore it.
	Action string
}

// Notifier posts and retracts desktop notices. Backends run their argv through
// the injected Runner, which always applies a timeout.
type Notifier interface {
	Name() string
	Capabilities() Capabilities
	// Post returns the server's id for the notice (0 when the backend has none).
	Post(Notice) (uint32, error)
	Retract(id uint32, group string) error
}
