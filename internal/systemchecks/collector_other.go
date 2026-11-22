//go:build !linux

package systemchecks

import (
	"404-probe/internal/protocol"
	"context"
	"errors"
)

func (*Collector) collect(context.Context, protocol.SystemCheckSettings, string, string) protocol.SystemChecks {
	return protocol.SystemChecks{}
}

func RunRoot(context.Context, bool) error { return errors.New("root collection requires Debian Linux") }
