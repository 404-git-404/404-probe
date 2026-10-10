package buildinfo

import (
	"debug/elf"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInspectLinkedLinuxReleaseNeverExecutesAndBindsVersion(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	command := exec.Command("git", "rev-parse", "HEAD")
	command.Dir = root
	raw, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.TrimSpace(string(raw))
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agent")
			command := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-trimpath", "-ldflags=-w -X 404-probe/internal/buildinfo.Version=v1.0.1 -X 404-probe/internal/buildinfo.Commit="+commit, "-o", path, "./cmd/agent")
			command.Dir = root
			command.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0")
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("cross-build: %v %s", err, output)
			}
			inspected, err := InspectLinkedRelease(path)
			if err != nil || inspected.Version != "v1.0.1" || inspected.Commit != commit || inspected.GOARCH != arch {
				t.Fatalf("static identity %+v %v", inspected, err)
			}
			f, err := elf.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			symbols, err := f.Symbols()
			if err != nil {
				t.Fatal(err)
			}
			var headerOffset int64
			for _, symbol := range symbols {
				if symbol.Name == "404-probe/internal/buildinfo.Version" {
					headerOffset = int64(f.Sections[symbol.Section].Offset + symbol.Value - f.Sections[symbol.Section].Addr)
				}
			}
			f.Close()
			if headerOffset == 0 {
				t.Fatal("Version header missing")
			}
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, kind := range []string{"bad-pointer", "bad-length", "wrong-version"} {
				mutated := append([]byte(nil), original...)
				switch kind {
				case "bad-pointer":
					binary.LittleEndian.PutUint64(mutated[headerOffset:headerOffset+8], ^uint64(0))
				case "bad-length":
					binary.LittleEndian.PutUint64(mutated[headerOffset+8:headerOffset+16], 65)
				case "wrong-version":
					ef, err := elf.Open(path)
					if err != nil {
						t.Fatal(err)
					}
					pointer := binary.LittleEndian.Uint64(mutated[headerOffset : headerOffset+8])
					found := false
					for _, section := range ef.Sections {
						if pointer >= section.Addr && pointer-section.Addr < section.Size && section.Type == elf.SHT_PROGBITS {
							offset := section.Offset + pointer - section.Addr
							copy(mutated[offset:offset+6], []byte("v1.0.2"))
							found = true
							break
						}
					}
					ef.Close()
					if !found {
						t.Fatal("Version string missing")
					}
				}
				candidate := filepath.Join(t.TempDir(), kind)
				if err := os.WriteFile(candidate, mutated, 0600); err != nil {
					t.Fatal(err)
				}
				info, err := InspectLinkedRelease(candidate)
				if kind == "wrong-version" {
					if err != nil || info.Version != "v1.0.2" || info.Commit != commit {
						t.Fatalf("wrong linked version obscured %+v %v", info, err)
					}
				} else if err == nil {
					t.Fatal("accepted", kind)
				}
			}
		})
	}
}
