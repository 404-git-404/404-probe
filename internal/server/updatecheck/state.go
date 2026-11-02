package updatecheck

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"404-probe/internal/buildinfo"
)

// New loads local state without making a network request. A missing persistence
// directory (in-memory databases) leaves checks disabled.
func New(path string, info buildinfo.Info, logger *slog.Logger) *Checker {
	c := newChecker(State{}, info, logger)
	if path != "" {
		state, err := load(path)
		if err != nil {
			c.warn("state unreadable; update checks disabled")
		} else {
			c.state = state
		}
	}
	c.persist = func(s State) error { return save(path, s) }
	return c
}

// OpenRoot confines every operation to the state directory. Lstat/fstat and
// SameFile reject symlinks and substitution before any bytes are read. Rename
// replaces the directory entry itself; it never opens a preexisting destination.
func load(path string) (State, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return State{}, err
	}
	defer root.Close()
	name := filepath.Base(path)
	before, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return State{Enabled: true}, nil
	}
	if err != nil {
		return State{}, err
	}
	if !safeStateFile(before) {
		return State{}, errors.New("unsafe state file")
	}
	f, err := root.Open(name)
	if err != nil {
		return State{}, err
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil {
		return State{}, err
	}
	if !os.SameFile(before, after) || !safeStateFile(after) {
		return State{}, errors.New("state file changed")
	}
	body, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return State{}, err
	}
	if len(body) > 4096 {
		return State{}, errors.New("state file too large")
	}
	var wire struct {
		Enabled   *bool  `json:"enabled"`
		Latest    string `json:"latest"`
		CheckedAt int64  `json:"checked_at"`
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if d.Decode(&wire) != nil || wire.Enabled == nil || wire.CheckedAt < 0 || (wire.Latest != "" && (!buildinfo.IsCanonicalVersion(wire.Latest) || wire.CheckedAt == 0)) || (wire.Latest == "" && wire.CheckedAt != 0) {
		return State{}, errors.New("invalid state file")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return State{}, errors.New("invalid state file suffix")
	}
	return State{Enabled: *wire.Enabled, Latest: wire.Latest, CheckedAt: wire.CheckedAt}, nil
}

func save(path string, s State) error {
	if path == "" {
		return errors.New("state directory unavailable")
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer root.Close()
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return err
	}
	name := ".update-check-" + hex.EncodeToString(nonce[:]) + ".tmp"
	f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(name)
	wire := struct {
		Enabled   bool   `json:"enabled"`
		Latest    string `json:"latest"`
		CheckedAt int64  `json:"checked_at"`
	}{s.Enabled, s.Latest, s.CheckedAt}
	err = json.NewEncoder(f).Encode(wire)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = root.Rename(name, filepath.Base(path)); err != nil {
		return err
	}
	return syncDirectory(root)
}
