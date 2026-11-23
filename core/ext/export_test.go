package ext

import "time"

// SetTimeout replaces extensions.timeout, for tests that need less than a
// second.
func (m *Manager) SetTimeout(d time.Duration) { m.testTimeout = d }

// SetRestartDelay replaces the wait before restarting a crashed extension.
func (m *Manager) SetRestartDelay(d time.Duration) { m.restartDelay = d }
