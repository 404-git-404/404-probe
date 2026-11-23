package backup

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"404-probe/internal/storage"
	"filippo.io/age"
	"github.com/klauspost/compress/zstd"
)

const (
	DatabaseName     = "404-probe.db"
	EnvironmentName  = "server.env"
	ControlTokenName = "control.token"
	PasswordHashName = "web-password.hash"
	UpdateCheckName  = "update-check.json"
	manifestName     = "manifest.json"
	archiveFormat    = 1
	maxConfigSize    = 64 << 10
	maxSnapshotSize  = int64(32 << 30)
)

var configNames = []string{EnvironmentName, ControlTokenName, PasswordHashName, UpdateCheckName}

type archiveManifest struct {
	Format int `json:"format"`
	Schema int `json:"schema"`
}

type stagedArchive struct {
	path   string
	digest string
	size   int64
	schema int
}

func stageArchive(ctx context.Context, store *storage.Store, dir string, configPaths map[string]string, publicKey string) (stagedArchive, error) {
	recipient, err := age.ParseX25519Recipient(publicKey)
	if err != nil {
		return stagedArchive{}, errors.New("invalid age recipient")
	}
	snapshot := filepath.Join(dir, DatabaseName)
	schema, err := store.BackupSnapshot(ctx, snapshot)
	if err != nil {
		return stagedArchive{}, fmt.Errorf("database snapshot: %w", err)
	}
	paths := map[string]string{DatabaseName: snapshot}
	for _, name := range configNames {
		if path := configPaths[name]; path != "" {
			paths[name] = path
		}
	}
	path := filepath.Join(dir, "upload.age")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return stagedArchive{}, errors.New("could not create encrypted backup working file")
	}
	hash := sha256.New()
	output := &workingWriter{ctx: ctx, dir: dir, dst: io.MultiWriter(file, hash), limit: maxObjectBytes}
	err = writeArchive(output, recipient, schema, paths)
	if err := errors.Join(err, file.Close()); err != nil {
		return stagedArchive{}, err
	}
	return stagedArchive{path, hex.EncodeToString(hash.Sum(nil)), output.written, schema}, nil
}

func writeArchive(out io.Writer, recipient age.Recipient, schema int, paths map[string]string) error {
	encrypted, err := age.Encrypt(out, recipient)
	if err != nil {
		return err
	}
	compressed, err := zstd.NewWriter(encrypted, zstd.WithEncoderConcurrency(1), zstd.WithEncoderLevel(zstd.SpeedFastest))
	if err != nil {
		return err
	}
	tarred := tar.NewWriter(compressed)
	manifest, err := json.Marshal(archiveManifest{archiveFormat, schema})
	if err != nil {
		return err
	}
	if err := tarred.WriteHeader(tarHeader(manifestName, int64(len(manifest)))); err != nil {
		return err
	}
	if _, err := tarred.Write(manifest); err != nil {
		return err
	}
	for _, name := range append([]string{DatabaseName}, configNames...) {
		if path := paths[name]; path != "" {
			if err := addFile(tarred, name, path); err != nil {
				return err
			}
		}
	}
	if err := tarred.Close(); err != nil {
		return err
	}
	if err := compressed.Close(); err != nil {
		return err
	}
	return encrypted.Close()
}

func tarHeader(name string, size int64) *tar.Header {
	return &tar.Header{Name: name, Size: size, Mode: 0600, Typeflag: tar.TypeReg}
}

func addFile(out *tar.Writer, name, path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && name == UpdateCheckName {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > fileLimit(name) {
		return fmt.Errorf("backup input %s is unavailable or too large", name)
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("could not read backup input %s", name)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return fmt.Errorf("backup input %s changed before it could be read", name)
	}
	if err := out.WriteHeader(tarHeader(name, info.Size())); err != nil {
		return err
	}
	_, err = io.CopyN(out, f, info.Size())
	return err
}

func fileLimit(name string) int64 {
	if name == DatabaseName {
		return maxSnapshotSize
	}
	return maxConfigSize
}

type workingWriter struct {
	ctx     context.Context
	dir     string
	dst     io.Writer
	limit   int64
	written int64
	checked int64
}

func (w *workingWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	end := w.written + int64(len(p))
	if end > w.limit {
		return 0, errors.New("backup exceeds the file size limit")
	}
	if end > w.checked {
		const interval = 1 << 20
		if err := storage.CheckBackupSpace(w.dir, int64(len(p))+interval); err != nil {
			return 0, err
		}
		w.checked = end + interval
	}
	n, err := w.dst.Write(p)
	w.written += int64(n)
	return n, err
}

// Extract validates the entire encrypted stream, archive layout and SQLite
// integrity before publishing the new directory. An existing path is rejected.
func Extract(ctx context.Context, src io.Reader, identity age.Identity, dir string) (verification storage.DatabaseVerification, err error) {
	if err := os.Mkdir(dir, 0700); err != nil {
		return verification, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.RemoveAll(dir))
		}
	}()
	limited := &io.LimitedReader{R: src, N: maxObjectBytes + 1}
	plaintext, err := age.Decrypt(limited, identity)
	if err != nil {
		var noMatch *age.NoIdentityMatchError
		if errors.As(err, &noMatch) {
			return verification, errors.New("the age private key cannot decrypt this backup")
		}
		return verification, errors.New("invalid or damaged age backup header")
	}
	compressed, err := zstd.NewReader(plaintext, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(64<<20))
	if err != nil {
		return verification, errors.New("invalid backup compression")
	}
	defer compressed.Close()
	archive := tar.NewReader(compressed)
	manifestHeader, err := archive.Next()
	if err != nil {
		return verification, errors.New("damaged backup or missing backup manifest")
	}
	if manifestHeader.Name != manifestName || manifestHeader.Typeflag != tar.TypeReg || manifestHeader.Size > 4096 {
		return verification, errors.New("invalid or missing backup manifest")
	}
	var manifest archiveManifest
	data, err := io.ReadAll(archive)
	if err != nil || json.Unmarshal(data, &manifest) != nil || manifest.Format != archiveFormat || manifest.Schema <= 0 {
		return verification, errors.New("invalid or unsupported backup manifest")
	}
	seen := make(map[string]bool)
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || header.Typeflag != tar.TypeReg || header.Size > fileLimit(header.Name) ||
			(header.Name != DatabaseName && !slices.Contains(configNames, header.Name)) || seen[header.Name] {
			return verification, errors.New("invalid, damaged or unexpected file in backup")
		}
		seen[header.Name] = true
		if err := extractFile(ctx, archive, dir, header); err != nil {
			return verification, err
		}
	}
	// tar reaches EOF before the outer streams do. Force verification of the
	// age final chunk and reject appended content or truncation.
	var trailing [1]byte
	if n, err := io.ReadFull(compressed, trailing[:]); n != 0 || !errors.Is(err, io.EOF) || limited.N == 0 {
		return verification, errors.New("damaged backup or unexpected trailing content")
	}
	if !seen[DatabaseName] {
		return verification, errors.New("backup does not contain a database")
	}
	verification, err = storage.VerifyReadOnly(ctx, filepath.Join(dir, DatabaseName))
	if err != nil {
		return verification, fmt.Errorf("verify backup database: %w", err)
	}
	if verification.SchemaVersion != manifest.Schema {
		return verification, errors.New("backup manifest schema does not match the database")
	}
	return verification, nil
}

func extractFile(ctx context.Context, in io.Reader, dir string, header *tar.Header) error {
	if err := storage.CheckBackupSpace(dir, header.Size); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, header.Name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	output := &workingWriter{ctx: ctx, dir: dir, dst: f, limit: fileLimit(header.Name)}
	_, err = io.Copy(output, in)
	return errors.Join(err, f.Close())
}

func archiveName(at time.Time, schema int) string {
	return fmt.Sprintf("%s%s-schema%d%s", objectStem, at.UTC().Format("20060102T150405.000000000Z"), schema, objectSuffix)
}
