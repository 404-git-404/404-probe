package storage

import "time"

// QualityClockSkewed flags an estimated offset in either direction. Upload
// buffering and transit are included in the estimate.
func QualityClockSkewed(offset *int64) bool {
	return offset != nil && (*offset > time.Minute.Milliseconds() || *offset < -time.Minute.Milliseconds())
}
