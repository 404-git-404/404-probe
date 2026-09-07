package securitycollector

import (
	"fmt"
	"os"
)

type collectorLock struct{ file *os.File }

func acquireCollectorLock(path string) (*collectorLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(file); err != nil {
		file.Close()
		return nil, fmt.Errorf("security collector already running: %w", err)
	}
	return &collectorLock{file: file}, nil
}

func (l *collectorLock) Close() error {
	unlockFile(l.file)
	return l.file.Close()
}
