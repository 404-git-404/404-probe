//go:build linux

package updater

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func runtimeDirectoryTestParent(t *testing.T) string {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("isolated root directory protection test")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0755); err != nil {
		t.Fatal(err)
	}
	return parent
}

func runtimeDirectoryStat(t *testing.T, path string) unix.Stat_t {
	t.Helper()
	var stat unix.Stat_t
	if err := unix.Lstat(path, &stat); err != nil {
		t.Fatal(err)
	}
	return stat
}

func TestRuntimeDirectoryCreatesAndBindsAfterServiceCleanup(t *testing.T) {
	parent := runtimeDirectoryTestParent(t)
	path := filepath.Join(parent, "agent-runtime")
	if err := os.Mkdir(path, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	} // old RuntimeDirectory cleanup
	mask := unix.Umask(0077)
	err := prepareAgentRuntimeDirectory(parent, "agent-runtime", 1234)
	unix.Umask(mask)
	if err != nil {
		t.Fatal(err)
	}
	stat := runtimeDirectoryStat(t, path)
	if stat.Uid != 0 || stat.Gid != 0 || stat.Mode&07777 != 0755 {
		t.Fatal("new stock runtime permissions", stat)
	}
	socket := filepath.Join(path, "agent-updater.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Chown(socket, 0, 1234); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(socket, 0660); err != nil {
		t.Fatal(err)
	}
	sock := runtimeDirectoryStat(t, socket)
	if sock.Mode&unix.S_IFMT != unix.S_IFSOCK || sock.Uid != 0 || sock.Gid != 1234 || sock.Mode&0777 != 0660 {
		t.Fatal(sock)
	}
}

func TestRuntimeDirectoryPreservesExistingSafeInodeContentAndPermissions(t *testing.T) {
	for _, mode := range []os.FileMode{0755, 0751, 0711, 0750} {
		t.Run(mode.String(), func(t *testing.T) {
			parent := runtimeDirectoryTestParent(t)
			path := filepath.Join(parent, "agent-runtime")
			if err := os.Mkdir(path, mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chown(path, 0, 1234); err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(path, "sentinel")
			if err := os.WriteFile(sentinel, []byte("unchanged"), 0600); err != nil {
				t.Fatal(err)
			}
			before := runtimeDirectoryStat(t, path)
			if err := prepareAgentRuntimeDirectory(parent, "agent-runtime", 1234); err != nil {
				t.Fatal(err)
			}
			after := runtimeDirectoryStat(t, path)
			if before.Dev != after.Dev || before.Ino != after.Ino || before.Mode != after.Mode || before.Uid != after.Uid || before.Gid != after.Gid {
				t.Fatal("existing directory changed", before, after)
			}
			if data, err := os.ReadFile(sentinel); err != nil || string(data) != "unchanged" {
				t.Fatal("content changed", err)
			}
		})
	}
}

func TestRuntimeDirectoryRejectsUnsafeOrInaccessibleTargetsWithoutRepair(t *testing.T) {
	for _, kind := range []string{"symlink", "dangling-symlink", "file", "group-writable", "world-writable", "wrong-owner", "inaccessible"} {
		t.Run(kind, func(t *testing.T) {
			parent := runtimeDirectoryTestParent(t)
			path := filepath.Join(parent, "agent-runtime")
			outside := t.TempDir()
			if err := os.Chmod(outside, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(outside, "sentinel"), []byte("untouched"), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "symlink":
				if err := os.Symlink(outside, path); err != nil {
					t.Fatal(err)
				}
			case "dangling-symlink":
				if err := os.Symlink(filepath.Join(outside, "absent"), path); err != nil {
					t.Fatal(err)
				}
			case "file":
				if err := os.WriteFile(path, []byte("keep file"), 0600); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.Mkdir(path, 0755); err != nil {
					t.Fatal(err)
				}
				mode := os.FileMode(0755)
				if kind == "group-writable" {
					mode = 0775
				}
				if kind == "world-writable" {
					mode = 0757
				}
				if kind == "inaccessible" {
					mode = 0700
				}
				if err := os.Chmod(path, mode); err != nil {
					t.Fatal(err)
				}
				if kind == "wrong-owner" {
					if err := os.Chown(path, 1234, 1234); err != nil {
						t.Fatal(err)
					}
				}
			}
			before := runtimeDirectoryStat(t, path)
			outsideBefore := runtimeDirectoryStat(t, outside)
			if err := prepareAgentRuntimeDirectory(parent, "agent-runtime", 4321); err == nil {
				t.Fatal("unsafe target accepted", kind)
			}
			after := runtimeDirectoryStat(t, path)
			outsideAfter := runtimeDirectoryStat(t, outside)
			if before.Dev != after.Dev || before.Ino != after.Ino || before.Mode != after.Mode || before.Uid != after.Uid || before.Gid != after.Gid {
				t.Fatal("unsafe target repaired", kind)
			}
			if outsideBefore.Ino != outsideAfter.Ino || outsideBefore.Mode != outsideAfter.Mode || outsideBefore.Uid != outsideAfter.Uid || outsideBefore.Gid != outsideAfter.Gid {
				t.Fatal("symlink target changed")
			}
			if data, err := os.ReadFile(filepath.Join(outside, "sentinel")); err != nil || string(data) != "untouched" {
				t.Fatal(err)
			}
		})
	}
}

func TestRuntimeDirectoryRejectsUnsafeParents(t *testing.T) {
	for _, kind := range []string{"symlink", "file", "wrong-owner", "group-writable", "world-writable", "inaccessible"} {
		t.Run(kind, func(t *testing.T) {
			root := runtimeDirectoryTestParent(t)
			parent := filepath.Join(root, "parent")
			outside := t.TempDir()
			if err := os.Chmod(outside, 0755); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "symlink":
				if err := os.Symlink(outside, parent); err != nil {
					t.Fatal(err)
				}
			case "file":
				if err := os.WriteFile(parent, []byte("keep parent"), 0600); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.Mkdir(parent, 0755); err != nil {
					t.Fatal(err)
				}
				mode := os.FileMode(0755)
				if kind == "group-writable" {
					mode = 0775
				}
				if kind == "world-writable" {
					mode = 0757
				}
				if kind == "inaccessible" {
					mode = 0700
				}
				if err := os.Chmod(parent, mode); err != nil {
					t.Fatal(err)
				}
				if kind == "wrong-owner" {
					if err := os.Chown(parent, 1234, 1234); err != nil {
						t.Fatal(err)
					}
				}
			}
			before := runtimeDirectoryStat(t, parent)
			if err := prepareAgentRuntimeDirectory(parent, "agent-runtime", 4321); err == nil {
				t.Fatal("unsafe parent accepted", kind)
			}
			after := runtimeDirectoryStat(t, parent)
			if before.Ino != after.Ino || before.Mode != after.Mode || before.Uid != after.Uid || before.Gid != after.Gid {
				t.Fatal("parent changed")
			}
			if _, err := os.Lstat(filepath.Join(outside, "agent-runtime")); !os.IsNotExist(err) {
				t.Fatal("followed parent symlink", err)
			}
		})
	}
}
