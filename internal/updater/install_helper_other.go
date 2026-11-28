//go:build !linux

package updater

import (
	"context"
	"errors"
	"os"
)

var errInstallHelperUnsupported = errors.New("management helper updates require Linux")

func ApplyStagedInstallHelper() error { return errInstallHelperUnsupported }

func renderInstallHelper(context.Context, []byte) ([]byte, error) {
	return nil, errInstallHelperUnsupported
}

func replaceInstallHelper(string, []byte, os.FileInfo) error { return errInstallHelperUnsupported }
