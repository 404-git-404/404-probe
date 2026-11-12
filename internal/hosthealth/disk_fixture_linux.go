//go:build disk_health_fixture

package hosthealth

import "time"

// CheckFixture runs the same probe on demand from the dedicated root harness.
func (m *Monitor) CheckFixture() { m.check() }

// AgeExt4Fixture advances only the elapsed fault window after real recovery.
func (m *Monitor) AgeExt4Fixture() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.ext4Observed.IsZero() {
		m.ext4Observed = m.ext4Observed.Add(-ext4Window - time.Second)
	}
}
