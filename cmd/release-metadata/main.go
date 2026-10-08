package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"404-probe/internal/buildinfo"
	"404-probe/internal/releasemetadata"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("release-metadata", flag.ContinueOnError)
	version := flags.String("version", "", "canonical release version")
	commit := flags.String("commit", "", "release commit")
	output := flags.String("output", "", "release output directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !buildinfo.IsReleaseVersion(*version) || !releasemetadata.IsCommit(*commit) || *output == "" || len(flags.Args()) == 0 {
		return errors.New("version, commit, output, and release asset paths are required")
	}
	document := releasemetadata.Document{SchemaVersion: releasemetadata.SchemaVersion, Version: *version, Commit: *commit}
	checksums := make(map[string]string, len(flags.Args()))
	for _, path := range flags.Args() {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("release asset %q is not a regular file", path)
		}
		name := filepath.Base(path)
		goos, goarch, ok := releasemetadata.PlatformForAsset(name)
		if !ok {
			return fmt.Errorf("release asset %q has an unsupported name", name)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read release asset %q: %w", name, err)
		}
		digest := sha256.Sum256(data)
		encoded := hex.EncodeToString(digest[:])
		checksums[name] = encoded
		document.Assets = append(document.Assets, releasemetadata.Asset{Name: name, GOOS: goos, GOARCH: goarch, SHA256: encoded})
	}
	metadata, err := releasemetadata.Encode(document)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(checksums))
	for name := range checksums {
		names = append(names, name)
	}
	sort.Strings(names)
	var sums strings.Builder
	for _, name := range names {
		fmt.Fprintf(&sums, "%s  %s\n", checksums[name], name)
	}
	if err := os.WriteFile(filepath.Join(*output, "RELEASE-METADATA.json"), metadata, 0644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(*output, "SHA256SUMS"), []byte(sums.String()), 0644)
}
