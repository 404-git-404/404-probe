package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"time"

	"404-probe/internal/auth"
	"404-probe/internal/storage"
)

func webCredentialCommand(args []string) error {
	const usage = "usage: 404-probe-server web-credential adopt --db path --hash-file path"
	if len(args) == 0 || args[0] != "adopt" {
		return errors.New(usage)
	}
	flags := flag.NewFlagSet("web-credential adopt", flag.ContinueOnError)
	db := flags.String("db", "", "existing Server database")
	hashFile := flags.String("hash-file", "", "private Web password hash file")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *db == "" || *hashFile == "" || flags.NArg() != 0 {
		return errors.New(usage)
	}
	if info, err := os.Lstat(*db); err != nil || !info.Mode().IsRegular() {
		return errors.New("existing Server database must be a regular file")
	}
	file, err := readWebPasswordFile(*hashFile)
	if err != nil {
		return err
	}
	store, err := openStore(*db)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.AdoptWebCredential(context.Background(), file.PasswordHash, file.FileSHA256, time.Now()); err != nil {
		return errors.New("could not adopt Web credential")
	}
	return nil
}

func reconcileWebPasswordFile(ctx context.Context, store *storage.Store, path string, now time.Time) (storage.WebCredential, error) {
	credential, err := store.WebCredential(ctx)
	if err != nil {
		return storage.WebCredential{}, errors.New("could not read Web credential")
	}
	// Read the file after acquiring the expected revision. A concurrent adopt
	// may commit a newer revision, which the conditional update won't overwrite.
	file, err := readWebPasswordFile(path)
	if err != nil {
		return storage.WebCredential{}, err
	}
	if credential.Revision == 0 {
		return file, nil
	}
	if credential.FileSHA256 != file.FileSHA256 {
		if _, err := store.ReplaceWebCredential(ctx, credential.Revision, file.PasswordHash, file.FileSHA256, now); err != nil {
			return storage.WebCredential{}, errors.New("could not adopt the replaced Web password file")
		}
		return file, nil
	}
	if auth.ParsePasswordHash(credential.PasswordHash) != nil {
		return storage.WebCredential{}, errors.New("stored Web credential is invalid")
	}
	return file, nil
}
