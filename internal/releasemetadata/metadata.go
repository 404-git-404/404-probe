package releasemetadata

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"404-probe/internal/buildinfo"
)

const SchemaVersion = 1

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

type Document struct {
	SchemaVersion int     `json:"schema_version"`
	Version       string  `json:"version"`
	Commit        string  `json:"commit"`
	Assets        []Asset `json:"assets"`
}

type Asset struct {
	Name   string `json:"name"`
	GOOS   string `json:"goos"`
	GOARCH string `json:"goarch"`
	SHA256 string `json:"sha256"`
}

func Decode(data []byte) (Document, error) {
	if err := rejectDuplicateObjectKeys(data); err != nil {
		return Document{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var document Document
	if err := decoder.Decode(&document); err != nil {
		return Document{}, errors.New("release metadata is not strict JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Document{}, errors.New("release metadata contains trailing data")
	}
	if err := Validate(document); err != nil {
		return Document{}, err
	}
	return document, nil
}

func rejectDuplicateObjectKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("release metadata object key is invalid")
				}
				if _, exists := seen[key]; exists {
					return fmt.Errorf("release metadata contains duplicate key %q", key)
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return errors.New("release metadata contains an unexpected delimiter")
		}
	}
	if err := walk(); err != nil {
		return errors.New("release metadata is not strict JSON")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("release metadata contains trailing data")
	}
	return nil
}

func Validate(document Document) error {
	if document.SchemaVersion != SchemaVersion {
		return errors.New("unsupported release metadata schema")
	}
	if !buildinfo.IsCanonicalVersion(document.Version) {
		return errors.New("release metadata version is invalid")
	}
	if !IsCommit(document.Commit) {
		return errors.New("release metadata commit is invalid")
	}
	if len(document.Assets) == 0 {
		return errors.New("release metadata has no assets")
	}
	seen := make(map[string]struct{}, len(document.Assets))
	for _, asset := range document.Assets {
		goos, goarch, ok := PlatformForAsset(asset.Name)
		if !ok || asset.GOOS != goos || asset.GOARCH != goarch {
			return fmt.Errorf("release metadata asset %q has an invalid platform", asset.Name)
		}
		if _, exists := seen[asset.Name]; exists {
			return fmt.Errorf("release metadata asset %q is duplicated", asset.Name)
		}
		seen[asset.Name] = struct{}{}
		if len(asset.SHA256) != 64 || strings.ToLower(asset.SHA256) != asset.SHA256 {
			return fmt.Errorf("release metadata asset %q has an invalid SHA256", asset.Name)
		}
		if _, err := hex.DecodeString(asset.SHA256); err != nil {
			return fmt.Errorf("release metadata asset %q has an invalid SHA256", asset.Name)
		}
	}
	return nil
}

func Select(document Document, target, name, goos, goarch string) (Asset, error) {
	if document.Version != target {
		return Asset{}, errors.New("release metadata version does not match target")
	}
	var selected *Asset
	for index := range document.Assets {
		asset := &document.Assets[index]
		if asset.Name != name {
			continue
		}
		if selected != nil {
			return Asset{}, errors.New("release metadata contains duplicate target assets")
		}
		selected = asset
	}
	if selected == nil {
		return Asset{}, errors.New("release metadata target asset is missing")
	}
	if selected.GOOS != goos || selected.GOARCH != goarch {
		return Asset{}, errors.New("release metadata target platform does not match")
	}
	return *selected, nil
}

func Encode(document Document) ([]byte, error) {
	if err := Validate(document); err != nil {
		return nil, err
	}
	sort.Slice(document.Assets, func(i, j int) bool { return document.Assets[i].Name < document.Assets[j].Name })
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func IsCommit(value string) bool { return commitPattern.MatchString(value) }

func PlatformForAsset(name string) (string, string, bool) {
	for _, role := range []string{"agent", "server"} {
		for _, arch := range []string{"amd64", "arm64"} {
			if name == "404-probe-"+role+"-linux-"+arch {
				return "linux", arch, true
			}
		}
	}
	return "", "", false
}
