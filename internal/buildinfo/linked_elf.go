package buildinfo

import (
	"debug/elf"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
)

// InspectLinkedRelease only reads the executable. It never runs a candidate.
func InspectLinkedRelease(path string) (ExecutableInfo, error) {
	fileInfo, err := os.Stat(path)
	if err != nil || !fileInfo.Mode().IsRegular() || fileInfo.Size() <= 0 || fileInfo.Size() > 128<<20 {
		return ExecutableInfo{}, errors.New("invalid release file size/type")
	}
	info, err := InspectExecutable(path)
	if err != nil {
		return ExecutableInfo{}, err
	}
	if info.GOOS != "linux" {
		return ExecutableInfo{}, errors.New("linked release inspection requires Linux")
	}
	f, err := elf.Open(path)
	if err != nil {
		return ExecutableInfo{}, err
	}
	defer f.Close()
	count := 0
	for _, section := range f.Sections {
		if section.Addr > math.MaxUint64-section.Size || section.Offset > uint64(fileInfo.Size()) || section.Type != elf.SHT_NOBITS && section.Size > uint64(fileInfo.Size())-section.Offset {
			return ExecutableInfo{}, errors.New("invalid release section range")
		}
		if section.Type == elf.SHT_SYMTAB {
			count++
			if section.Size > 16<<20 || int(section.Link) >= len(f.Sections) || f.Sections[section.Link].Size > 16<<20 {
				return ExecutableInfo{}, errors.New("release symbol table exceeds limit")
			}
		}
	}
	if count != 1 {
		return ExecutableInfo{}, errors.New("ambiguous release symbol table")
	}
	if f.Class != elf.ELFCLASS64 || f.Data != elf.ELFDATA2LSB || f.Type != elf.ET_EXEC ||
		!(info.GOARCH == "amd64" && f.Machine == elf.EM_X86_64 || info.GOARCH == "arm64" && f.Machine == elf.EM_AARCH64) {
		return ExecutableInfo{}, errors.New("unsupported release ELF layout")
	}
	symbols, err := f.Symbols()
	if err != nil {
		return ExecutableInfo{}, errors.New("release symbols missing")
	}
	read := func(name string) (string, error) {
		var found *elf.Symbol
		for i := range symbols {
			if symbols[i].Name == name {
				if found != nil {
					return "", errors.New("ambiguous release symbol")
				}
				found = &symbols[i]
			}
		}
		if found == nil || elf.ST_BIND(found.Info) != elf.STB_GLOBAL || elf.ST_TYPE(found.Info) != elf.STT_OBJECT || found.Other != 0 || found.Size != 16 || found.Value%8 != 0 || int(found.Section) >= len(f.Sections) {
			return "", errors.New("invalid release string symbol")
		}
		s := f.Sections[found.Section]
		if s.Type != elf.SHT_PROGBITS || s.Flags&(elf.SHF_ALLOC|elf.SHF_WRITE) != elf.SHF_ALLOC|elf.SHF_WRITE || s.Flags&elf.SHF_EXECINSTR != 0 || found.Value < s.Addr || found.Value-s.Addr > s.Size || 16 > s.Size-(found.Value-s.Addr) {
			return "", errors.New("invalid release string header range")
		}
		if !releaseMappedRange(f, s, found.Value, 16, true) {
			return "", errors.New("release header is not mapped writable data")
		}
		header := make([]byte, 16)
		if _, err := s.ReadAt(header, int64(found.Value-s.Addr)); err != nil {
			return "", err
		}
		address, length := binary.LittleEndian.Uint64(header[:8]), binary.LittleEndian.Uint64(header[8:])
		if length == 0 || length > 64 {
			return "", errors.New("invalid release string length")
		}
		var section *elf.Section
		for _, candidate := range f.Sections {
			if candidate.Type == elf.SHT_PROGBITS && candidate.Flags&elf.SHF_ALLOC != 0 && candidate.Flags&(elf.SHF_WRITE|elf.SHF_EXECINSTR) == 0 && address >= candidate.Addr && address-candidate.Addr <= candidate.Size && length <= candidate.Size-(address-candidate.Addr) {
				if section != nil {
					return "", errors.New("overlapping release string range")
				}
				section = candidate
			}
		}
		if section == nil {
			return "", errors.New("release string is outside read-only sections")
		}
		if !releaseMappedRange(f, section, address, length, false) {
			return "", errors.New("release string is not mapped read-only data")
		}
		data := make([]byte, int(length))
		if _, err := section.ReadAt(data, int64(address-section.Addr)); err != nil {
			return "", err
		}
		return string(data), nil
	}
	version, err := read("404-probe/internal/buildinfo.Version")
	if err != nil {
		return ExecutableInfo{}, fmt.Errorf("release Version: %w", err)
	}
	commit, err := read("404-probe/internal/buildinfo.Commit")
	if err != nil {
		return ExecutableInfo{}, fmt.Errorf("release Commit: %w", err)
	}
	if !IsReleaseVersion(version) || len(commit) != 40 || commit != info.Commit {
		return ExecutableInfo{}, errors.New("linked release identity mismatch")
	}
	for _, c := range commit {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return ExecutableInfo{}, errors.New("invalid linked commit")
		}
	}
	info.Version = version
	return info, nil
}

func releaseMappedRange(f *elf.File, section *elf.Section, address, length uint64, writable bool) bool {
	if section.Flags&elf.SHF_COMPRESSED != 0 {
		return false
	}
	matches := 0
	for _, segment := range f.Progs {
		if segment.Type != elf.PT_LOAD || address < segment.Vaddr || address-segment.Vaddr > segment.Filesz || length > segment.Filesz-(address-segment.Vaddr) {
			continue
		}
		flags := elf.PF_R
		if writable {
			flags |= elf.PF_W
		}
		if segment.Flags != flags || segment.Off > math.MaxUint64-(address-segment.Vaddr) || segment.Off+(address-segment.Vaddr) != section.Offset+(address-section.Addr) {
			return false
		}
		matches++
	}
	return matches == 1
}
