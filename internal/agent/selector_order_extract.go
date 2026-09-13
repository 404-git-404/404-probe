package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maxSingBoxConfigBytes = 8 << 20

// ExtractSelectorOrder reads only the type and tag fields from the top-level
// sing-box outbounds array and writes a bounded, secret-free metadata document.
func ExtractSelectorOrder(configPath, outputPath string) error {
	if !filepath.IsAbs(configPath) || !filepath.IsAbs(outputPath) || configPath == outputPath {
		return errors.New("config and output must be distinct absolute paths")
	}
	file, err := os.Open(configPath)
	if err != nil {
		return fmt.Errorf("open sing-box config: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxSingBoxConfigBytes {
		return errors.New("sing-box config must be a non-empty regular file no larger than 8 MiB")
	}
	var config struct {
		Outbounds []struct {
			Type string `json:"type"`
			Tag  string `json:"tag"`
		} `json:"outbounds"`
	}
	decoder := json.NewDecoder(io.LimitReader(file, maxSingBoxConfigBytes+1))
	if err := decoder.Decode(&config); err != nil {
		return fmt.Errorf("decode sing-box config as standard JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("sing-box config contains trailing data")
	}
	seen := make(map[string]struct{})
	document := selectorOrderDocument{Selectors: make([]string, 0)}
	for _, outbound := range config.Outbounds {
		if !strings.EqualFold(outbound.Type, "selector") {
			continue
		}
		if strings.TrimSpace(outbound.Tag) != outbound.Tag || outbound.Tag == "" {
			return errors.New("selector outbound has an invalid tag")
		}
		if _, duplicate := seen[outbound.Tag]; duplicate {
			return errors.New("selector outbound tag is duplicated")
		}
		seen[outbound.Tag] = struct{}{}
		document.Selectors = append(document.Selectors, outbound.Tag)
	}
	if len(document.Selectors) == 0 {
		return errors.New("sing-box config contains no top-level selector outbounds")
	}
	body, err := json.Marshal(document)
	if err != nil {
		return err
	}
	body = append(body, '\n')
	directory := filepath.Dir(outputPath)
	temporary, err := os.CreateTemp(directory, ".selector-order-*")
	if err != nil {
		return fmt.Errorf("create selector order metadata: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o640); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(body); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, outputPath); err != nil {
		return fmt.Errorf("replace selector order metadata: %w", err)
	}
	return nil
}
