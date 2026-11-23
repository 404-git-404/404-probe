//go:build !linux

package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
)

func privateRestoreFile(os.FileInfo) bool { return true }

func restoreWorkDirectory() (string, error) {
	return "", errors.New("installed Server restore requires Linux; use --extract-to to inspect a backup")
}

func restoreInstalledBackup(context.Context, string, string, int, *bufio.Reader, io.Writer) error {
	return errors.New("installed Server restore requires Linux")
}
