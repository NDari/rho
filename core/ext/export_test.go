package ext

import "time"

// SetTimeout replaces extensions.timeout, for tests that need less than a
// second.
func (m *Manager) SetTimeout(d time.Duration) { m.testTimeout = d }
