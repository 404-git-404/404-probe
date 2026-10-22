package storage

import (
	"encoding/json"

	"404-probe/internal/protocol"
)

// qualityTargetFromPayload rebuilds a stored target. The payload of a user target carries no id: the row does
// (quality_targets.id), and everything keyed by a target - series, raw samples, buckets, the page's lanes -
// uses that id. Every place that reads a payload goes through here, so none can forget it; a test enforces it.
func qualityTargetFromPayload(id, payload string) (protocol.QualityTarget, error) {
	var target protocol.QualityTarget
	if err := json.Unmarshal([]byte(payload), &target); err != nil {
		return protocol.QualityTarget{}, err
	}
	target.ID = id
	return target, nil
}
