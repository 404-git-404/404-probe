package systemchecks

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

func settingsKey(value any) string {
	raw, _ := json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}
