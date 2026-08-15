package agent

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func nextEpoch(path string) (uint64, error) {
	path = filepath.Clean(path)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return 0, fmt.Errorf("create state directory: %w", err)
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return 0, fmt.Errorf("open state lock: %w", err)
	}
	defer lock.Close()
	if err := lockFile(lock); err != nil {
		return 0, fmt.Errorf("lock state: %w", err)
	}
	defer unlockFile(lock)

	var current uint64
	data, err := os.ReadFile(path)
	if err == nil {
		value := strings.TrimSpace(string(data))
		if value == "" {
			return 0, errors.New("state file is empty")
		}
		current, err = strconv.ParseUint(value, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse state file: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, fmt.Errorf("read state file: %w", err)
	}
	if current == math.MaxUint64 {
		return 0, errors.New("epoch exhausted")
	}
	next := current + 1
	if err := writeEpoch(path, dir, next); err != nil {
		return 0, err
	}
	return next, nil
}

func writeEpoch(path, dir string, epoch uint64) error {
	tmp, err := os.CreateTemp(dir, ".404-probe-epoch-*")
	if err != nil {
		return fmt.Errorf("create temporary state: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return fmt.Errorf("protect temporary state: %w", err)
	}
	if _, err := fmt.Fprintf(tmp, "%d\n", epoch); err != nil {
		tmp.Close()
		return fmt.Errorf("write temporary state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temporary state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary state: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace state file: %w", err)
	}
	if err := syncDirectory(dir); err != nil {
		return fmt.Errorf("sync state directory: %w", err)
	}
	return nil
}
