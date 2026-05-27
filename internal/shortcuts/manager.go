package shortcuts

// Manager pushes hotkey config to connected helpers.
// Push is a no-op until helper integration is implemented.
type Manager struct{}

// New returns a new Manager.
func New() *Manager { return &Manager{} }

// Push sends the current hotkeys to all connected helpers immediately.
// Called on every PUT /api/shortcuts and on each new helper connection.
func (m *Manager) Push(h map[string]string) error { return nil }
