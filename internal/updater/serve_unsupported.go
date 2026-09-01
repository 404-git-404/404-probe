//go:build !linux

package updater

import (
	"context"
	"errors"
)

func platformServe(context.Context) error {
	return errors.New("Agent updater is supported only on Linux")
}
