package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"404-probe/internal/protocol"
	"404-probe/internal/storage"
)

const (
	skinFileLimit = 64 << 10
	skinFormat    = "404-probe-skin"
	skinVersion   = 1
)

var (
	skinCSSComments = regexp.MustCompile(`(?s)/\*.*?\*/`)
	skinCSSRoot     = regexp.MustCompile(`(?m)^\s*:root\s*\{([^{}]*)\}`)
	skinCSSDarkRoot = regexp.MustCompile(`(?m)^\s*:root\s*\[\s*data-theme\s*=\s*(?:dark|"dark"|'dark')\s*\]\s*\{([^{}]*)\}`)
	skinCSSVariable = regexp.MustCompile(`^\s*(--(?:color|tag)-[a-zA-Z0-9_-]+)\s*:\s*(.+?)\s*$`)
	skinHex         = regexp.MustCompile(`^#(?:[0-9a-f]{3,4}|[0-9a-f]{6}|[0-9a-f]{8})$`)
	skinNumber      = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]+)?|\.[0-9]+)(?:e[+-]?[0-9]+)?$`)
	skinFunction    = regexp.MustCompile(`^(rgba?|hsla?)\(([0-9a-z%+.,/ \t-]+)\)$`)
)

var skinSwatchNames = [...]string{"--color-page", "--color-card", "--color-text", "--color-success", "--color-danger"}

type skinPalette struct {
	light, dark map[string]string
	allowed     map[string]bool
}

func parseSkinPalette(css []byte) (skinPalette, error) {
	palette := skinPalette{allowed: map[string]bool{}}
	css = skinCSSComments.ReplaceAll(css, nil)
	for _, mode := range []struct {
		root   *regexp.Regexp
		values *map[string]string
	}{{skinCSSRoot, &palette.light}, {skinCSSDarkRoot, &palette.dark}} {
		block := mode.root.FindSubmatch(css)
		if block == nil {
			return skinPalette{}, fmt.Errorf("skin variables: missing CSS root block")
		}
		values := map[string]string{}
		for _, declaration := range bytes.Split(block[1], []byte(";")) {
			match := skinCSSVariable.FindSubmatch(declaration)
			if match != nil {
				values[string(match[1])] = string(match[2])
				palette.allowed[string(match[1])] = true
			}
		}
		*mode.values = values
	}
	for _, name := range skinSwatchNames {
		if palette.light[name] == "" {
			return skinPalette{}, fmt.Errorf("skin variables: missing swatch %s", name)
		}
	}
	return palette, nil
}

type skinFile struct {
	Format      string            `json:"format"`
	Version     int               `json:"version"`
	Name        string            `json:"name"`
	Author      string            `json:"author"`
	Description string            `json:"description"`
	Light       map[string]string `json:"light"`
	Dark        map[string]string `json:"dark"`
}

func skinFileFrom(skin storage.Skin) skinFile {
	return skinFile{skinFormat, skinVersion, skin.Name, skin.Author, skin.Description, skin.Light, skin.Dark}
}

type skinImportError struct {
	Mode     string `json:"mode"`
	Variable string `json:"variable"`
	Reason   string `json:"reason"`
}

func (p skinPalette) parseFile(body []byte) (storage.Skin, []string, []skinImportError) {
	skin := storage.Skin{Light: map[string]string{}, Dark: map[string]string{}}
	var file struct {
		Format, Name, Author, Description string
		Version                           int
		Light, Dark                       json.RawMessage
	}
	if protocol.CheckQualityRequestEncoding(body) != nil || json.Unmarshal(body, &file) != nil {
		return skin, nil, []skinImportError{{"file", "", "invalid_json"}}
	}
	var problems []skinImportError
	if file.Format != skinFormat {
		problems = append(problems, skinImportError{"file", "format", "invalid_format"})
	}
	if file.Version != skinVersion {
		problems = append(problems, skinImportError{"file", "version", "invalid_version"})
	}
	if strings.TrimSpace(file.Name) == "" || utf8.RuneCountInString(file.Name) > 40 {
		problems = append(problems, skinImportError{"file", "name", "invalid_length"})
	}
	if utf8.RuneCountInString(file.Author) > 40 {
		problems = append(problems, skinImportError{"file", "author", "invalid_length"})
	}
	if utf8.RuneCountInString(file.Description) > 200 {
		problems = append(problems, skinImportError{"file", "description", "invalid_length"})
	}
	skin.Name, skin.Author, skin.Description = file.Name, file.Author, file.Description
	ignored := map[string]bool{}
	for _, mode := range []struct {
		name   string
		raw    json.RawMessage
		values map[string]string
	}{{"light", file.Light, skin.Light}, {"dark", file.Dark, skin.Dark}} {
		if mode.raw == nil {
			continue
		}
		var input map[string]json.RawMessage
		if json.Unmarshal(mode.raw, &input) != nil || input == nil {
			problems = append(problems, skinImportError{mode.name, "", "invalid_variables"})
			continue
		}
		for _, key := range slices.Sorted(maps.Keys(input)) {
			if !p.allowed[key] {
				ignored[key] = true
				continue
			}
			var raw string
			if json.Unmarshal(input[key], &raw) != nil {
				problems = append(problems, skinImportError{mode.name, key, "invalid_value"})
				continue
			}
			value, valid := normalizeSkinColor(raw)
			if !valid {
				problems = append(problems, skinImportError{mode.name, key, "invalid_value"})
				continue
			}
			mode.values[key] = value
		}
	}
	if len(skin.Light)+len(skin.Dark) == 0 && len(problems) == 0 {
		problems = append(problems, skinImportError{"file", "", "no_valid_variables"})
	}
	return skin, slices.Sorted(maps.Keys(ignored)), problems
}

func normalizeSkinColor(raw string) (string, bool) {
	if len(raw) == 0 || len(raw) > 64 {
		return "", false
	}
	value := strings.ToLower(strings.Trim(raw, " \t"))
	if value == "transparent" || skinHex.MatchString(value) {
		return value, true
	}
	match := skinFunction.FindStringSubmatch(value)
	if match == nil {
		return "", false
	}
	var channels []string
	var alpha string
	commaSyntax := strings.Contains(match[2], ",")
	if commaSyntax {
		channels = strings.Split(match[2], ",")
		if len(channels) == 4 {
			alpha, channels = strings.Trim(channels[3], " \t"), channels[:3]
			if alpha == "" {
				return "", false
			}
		}
	} else {
		channelsText, alphaText, hasAlpha := strings.Cut(match[2], "/")
		channels = strings.Fields(channelsText)
		alpha = strings.Trim(alphaText, " \t")
		if hasAlpha && alpha == "" {
			return "", false
		}
	}
	if len(channels) != 3 || (alpha != "" && !skinNumber.MatchString(strings.TrimSuffix(alpha, "%"))) {
		return "", false
	}
	hsl := strings.HasPrefix(match[1], "hsl")
	rgbPercent := strings.HasSuffix(strings.Trim(channels[0], " \t"), "%")
	for i, channel := range channels {
		channel = strings.Trim(channel, " \t")
		percent := strings.HasSuffix(channel, "%")
		if commaSyntax && ((!hsl && percent != rgbPercent) || (hsl && i > 0 && !percent)) {
			return "", false
		}
		if i == 0 && hsl {
			channel = strings.TrimSuffix(channel, "deg")
		} else {
			channel = strings.TrimSuffix(channel, "%")
		}
		if !skinNumber.MatchString(channel) {
			return "", false
		}
	}
	return value, true
}
