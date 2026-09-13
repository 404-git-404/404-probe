package agent

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"strings"

	"404-probe/internal/protocol"
)

const maxSelectorOrderBytes = 64 << 10

type selectorOrderDocument struct {
	Selectors []string `json:"selectors"`
}

func orderSelectors(selectors []protocol.OutboundSelector, path string) string {
	positions, ok := readSelectorOrder(path)
	if ok {
		for _, selector := range selectors {
			if _, found := positions[selector.Name]; !found {
				ok = false
				break
			}
		}
	}
	if !ok {
		sort.Slice(selectors, func(i, j int) bool { return selectors[i].Name < selectors[j].Name })
		return "name"
	}
	sort.SliceStable(selectors, func(i, j int) bool {
		return positions[selectors[i].Name] < positions[selectors[j].Name]
	})
	return "config"
}

func readSelectorOrder(path string) (map[string]int, bool) {
	if path == "" {
		return nil, false
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxSelectorOrderBytes {
		return nil, false
	}
	var document selectorOrderDocument
	decoder := json.NewDecoder(io.LimitReader(file, maxSelectorOrderBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil || len(document.Selectors) == 0 {
		return nil, false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, false
	}
	positions := make(map[string]int, len(document.Selectors))
	for index, name := range document.Selectors {
		if strings.TrimSpace(name) != name || name == "" {
			return nil, false
		}
		if _, duplicate := positions[name]; duplicate {
			return nil, false
		}
		positions[name] = index
	}
	return positions, true
}
