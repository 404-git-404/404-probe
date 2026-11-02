package updatecheck

import "os"

// Windows uses the state directory's inherited ACL; POSIX owner/mode assertions
// run on Linux. Regular-file and identity checks still apply on Windows.
func safeStateFile(info os.FileInfo) bool { return info.Mode().IsRegular() }
func syncDirectory(*os.Root) error        { return nil }
