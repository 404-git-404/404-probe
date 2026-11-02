package updatecheck

import (
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
)

func openStateFile(root *os.Root, name string) (*os.File, error) {
	path, err := windows.UTF16PtrFromString(filepath.Join(root.Name(), name))
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(path, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), name), nil
}

// Windows uses the state directory's inherited ACL; POSIX owner/mode assertions
// run on Linux. Regular-file and identity checks still apply on Windows.
func safeStateFile(info os.FileInfo) bool { return info.Mode().IsRegular() }
func syncDirectory(*os.Root) error        { return nil }
