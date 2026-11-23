package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"404-probe/internal/backup"
	"filippo.io/age"
)

const (
	installedDatabase = "/var/lib/404-probe/404-probe.db"
	installedConfig   = "/etc/404-probe"
)

func restoreBackupCommand(args []string, input io.Reader, output io.Writer) error {
	flags := flag.NewFlagSet("database restore-backup", flag.ContinueOnError)
	keyPath := flags.String("key", "", "age X25519 private key file")
	local := flags.String("file", "", "local .tar.zst.age backup")
	remote := flags.String("from-r2", "", "full R2 object name, including prefix")
	r2Path := flags.String("r2-config", "", "private R2 credentials JSON file for --from-r2")
	database := flags.String("db", "", "database to read saved R2 credentials from when only extracting")
	extract := flags.String("extract-to", "", "extract and verify into a new directory without replacing the installed Server")
	limit := flags.Duration("timeout", time.Hour, "time limit for download, extraction and verification")
	installedInit := flags.String("installed-init", "", "installed service manager (set by 404-probe-install)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *keyPath == "" || (*local == "") == (*remote == "") || *limit <= 0 ||
		(*remote == "" && (*r2Path != "" || *database != "")) || (*r2Path != "" && *database != "") ||
		(*installedInit != "" && *database != "") {
		return errors.New("usage: database restore-backup --key PATH (--file PATH | --from-r2 NAME [--r2-config PATH]) [--extract-to NEW_DIRECTORY]")
	}
	if *extract == "" && *installedInit == "" {
		return errors.New("use 404-probe-install for a service restore, or supply --extract-to")
	}
	identityFile, err := readRestoreInput(*keyPath, 4096, true)
	if err != nil {
		return fmt.Errorf("read age private key: %w", err)
	}
	keys, err := age.ParseIdentities(strings.NewReader(string(identityFile)))
	if err != nil || len(keys) != 1 {
		return errors.New("expected a file containing one age X25519 private key")
	}
	identity, ok := keys[0].(*age.X25519Identity)
	if !ok {
		return errors.New("expected an age X25519 private key")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	verifyCtx, cancel := context.WithTimeout(ctx, *limit)
	defer cancel()
	var source io.ReadCloser
	if *local != "" {
		source, err = os.Open(*local)
	} else {
		var config backup.R2Config
		if *r2Path != "" {
			config, err = readRestoreR2(*r2Path)
		} else {
			if *database == "" {
				*database = installedDatabase
			}
			config, err = backup.StoredR2(verifyCtx, *database)
		}
		if err == nil {
			source, err = backup.Download(verifyCtx, config, *remote)
		}
	}
	if err != nil {
		return fmt.Errorf("open backup: %w", err)
	}
	defer source.Close()
	dir := *extract
	if dir == "" {
		parent, err := restoreWorkDirectory()
		if err != nil {
			return err
		}
		defer os.RemoveAll(parent)
		dir = filepath.Join(parent, "verified")
	}
	verification, err := backup.Extract(verifyCtx, source, identity, dir)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output, "Verified backup: schema %d, SQLite integrity %s.\n", verification.SchemaVersion, verification.Integrity); err != nil {
		return err
	}
	if *extract != "" {
		_, err := fmt.Fprintf(output, "Extracted to %s.\n", dir)
		return err
	}
	return restoreInstalledBackup(ctx, dir, *installedInit, verification.SchemaVersion, bufio.NewReader(input), output)
}

func readRestoreR2(path string) (backup.R2Config, error) {
	var config backup.R2Config
	data, err := readRestoreInput(path, 8<<10, true)
	if err != nil {
		return config, fmt.Errorf("read R2 credential file: %w", err)
	}
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()
	if d.Decode(&config) != nil || !errors.Is(d.Decode(new(any)), io.EOF) {
		return config, errors.New("invalid R2 credential JSON")
	}
	return config, nil
}

func readRestoreInput(path string, maxSize int64, private bool) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxSize {
		return nil, errors.New("expected a small regular file")
	}
	if private && !privateRestoreFile(info) {
		return nil, errors.New("key and credential files must be private (chmod 600)")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("input file changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if int64(len(data)) > maxSize {
		return nil, errors.New("input file is too large")
	}
	return data, err
}
