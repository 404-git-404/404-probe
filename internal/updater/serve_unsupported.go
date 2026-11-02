//go:build !linux

package updater

import (
	"context"
	"errors"
)

func platformServe(context.Context) error {
	return errors.New("Agent updater is supported only on Linux")
}

func ServeAgentRemovalWorker(context.Context) error {
	return errors.New("Agent removal worker is supported only on Linux")
}

func ServeAgentRemovalFinalizer(context.Context) error {
	return errors.New("Agent removal finalizer is supported only on Linux")
}
