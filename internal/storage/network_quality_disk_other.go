//go:build !linux && !windows

package storage

import "errors"

func qualityFilesystem(string) (int64, int64, error) {
	return 0, 0, errors.New("quality disk observation unavailable")
}
func qualityRootFilesystem() string { return "/" }
