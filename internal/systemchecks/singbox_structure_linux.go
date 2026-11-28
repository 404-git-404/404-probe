package systemchecks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"404-probe/internal/protocol"
)

const singboxStructureFile = "singbox-structure.json"
const inventoryEnvelopeBytes = 4096

type singboxCache struct {
	ConfigDigest string                          `json:"config_digest"`
	Report       protocol.SingboxStructureReport `json:"report"`
}

func collectSingboxStructure(ctx context.Context, dir, defaultPath string, now time.Time, run commandRunner) protocol.SingboxStructureReport {
	result := protocol.SingboxStructureReport{Status: protocol.CheckError, CollectedAt: now.UnixMilli()}
	output, _ := run(ctx, "/usr/bin/systemctl", "show", "-p", "ExecStart", "sing-box.service")
	arguments := parseSingboxExecStart(output)
	if strings.Contains(output, "argv[]=") {
		// systemctl's text display flattens argv without quoting spaces in paths.
		// Read the same property over D-Bus when available to recover boundaries.
		busOutput, err := run(ctx, "/usr/bin/busctl", "--json=short", "get-property", "org.freedesktop.systemd1",
			"/org/freedesktop/systemd1/unit/sing_2dbox_2eservice", "org.freedesktop.systemd1.Service", "ExecStart")
		if argv := parseSingboxExecStartBus(busOutput); err == nil && len(argv) != 0 {
			arguments = argv
		}
	}
	paths, err := singboxConfigPaths(ctx, arguments, defaultPath)
	if err != nil {
		result.Reason = inventoryErrorReason(err)
		return result
	}
	var documents [][]byte
	remaining := int64(8 << 20)
	hash := sha256.New()
	for _, path := range paths {
		data, err := readInventoryBytes(ctx, path, remaining)
		if err != nil {
			result.Reason = inventoryErrorReason(err)
			return result
		}
		remaining -= int64(len(data))
		hash.Write(binary.BigEndian.AppendUint64(nil, uint64(len(data))))
		hash.Write(data)
		documents = append(documents, data)
	}
	configDigest := hex.EncodeToString(hash.Sum(nil))
	var cache singboxCache
	cachePath := filepath.Join(dir, "singbox-structure-cache.json")
	if readJSON(cachePath, protocol.MaxSingboxStructureBytes+inventoryEnvelopeBytes, &cache) == nil &&
		cache.ConfigDigest == configDigest && cache.Report.Status == protocol.CheckOK && cache.Report.Structure != nil && cache.Report.Validate() == nil {
		cache.Report.CollectedAt = result.CollectedAt
		return cache.Report
	}
	structure, truncated, err := extractSingboxStructure(ctx, documents)
	if err != nil {
		result.Reason = "parse_failed"
		if ctx.Err() != nil {
			result.Reason = "timeout"
		}
		return result
	}
	digest, size, err := protocol.InventoryDigest(structure)
	if err != nil || size > protocol.MaxSingboxStructureBytes {
		result.Reason = "too_large"
		return result
	}
	result.Status, result.Digest, result.Truncated, result.Structure = protocol.CheckOK, digest, truncated, structure
	// The source digest covers secrets and is only needed by the root collector.
	_ = writeJSON(cachePath, singboxCache{ConfigDigest: configDigest, Report: result}, 0600)
	return result
}

type configArgument struct {
	kind, path string
}

func singboxConfigPaths(ctx context.Context, arguments []string, defaultPath string) ([]string, error) {
	var inputs []configArgument
	base := "/" // A system service without WorkingDirectory starts in /.
	for i := 1; i < len(arguments); i++ {
		flag, value, hasValue := strings.Cut(arguments[i], "=")
		switch flag {
		case "-c", "--config", "-C", "--config-directory", "-D", "--directory":
			if !hasValue {
				i++
				if i == len(arguments) {
					return []string{defaultPath}, nil
				}
				value = arguments[i]
			}
			if value == "" || strings.ContainsRune(value, '\x00') {
				return []string{defaultPath}, nil
			}
			switch flag {
			case "-D", "--directory":
				base = value
				if !filepath.IsAbs(base) {
					base = filepath.Join("/", base)
				}
			case "-c", "--config":
				inputs = append(inputs, configArgument{"file", value})
			default:
				inputs = append(inputs, configArgument{"directory", value})
			}
		}
	}
	if len(inputs) == 0 {
		return []string{defaultPath}, nil
	}
	var paths []string
	for _, input := range inputs {
		path := input.path
		if !filepath.IsAbs(path) {
			path = filepath.Join(base, path)
		}
		if input.kind == "file" {
			paths = append(paths, path)
		} else {
			var names []string
			err := walkInventoryDirectory(ctx, path, 4096, func(name string) error {
				if filepath.Ext(name) == ".json" {
					info, err := os.Stat(filepath.Join(path, name))
					if err != nil {
						return err
					}
					if !info.IsDir() {
						names = append(names, name)
					}
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
			slices.Sort(names)
			for _, name := range names {
				paths = append(paths, filepath.Join(path, name))
			}
		}
		if len(paths) > 128 {
			return nil, errInventoryLimit
		}
	}
	if len(paths) == 0 {
		return nil, os.ErrNotExist
	}
	return paths, nil
}

func parseSingboxExecStartBus(output string) []string {
	var wire struct {
		Data [][]json.RawMessage `json:"data"`
	}
	var argv []string
	if json.Unmarshal([]byte(output), &wire) == nil && len(wire.Data) != 0 && len(wire.Data[0]) >= 2 {
		_ = json.Unmarshal(wire.Data[0][1], &argv)
	}
	return argv
}

func parseSingboxExecStart(output string) []string {
	_, args, ok := strings.Cut(output, "argv[]=")
	if !ok {
		return nil
	}
	args, _, _ = strings.Cut(args, " ; ignore_errors=")
	var fields []string
	var field strings.Builder
	var quote byte
	for i := 0; i < len(args); i++ {
		ch := args[i]
		switch {
		case ch == '\\' && quote != '\'':
			i++
			if i == len(args) {
				return nil
			}
			if args[i] == 'x' && i+2 < len(args) {
				if value, err := strconv.ParseUint(args[i+1:i+3], 16, 8); err == nil {
					field.WriteByte(byte(value))
					i += 2
					continue
				}
			}
			if args[i] == 's' {
				field.WriteByte(' ')
			} else {
				field.WriteByte(args[i])
			}
		case quote != 0:
			if ch == quote {
				quote = 0
			} else {
				field.WriteByte(ch)
			}
		case (ch == '\'' || ch == '"') && field.Len() == 0:
			quote = ch
		case ch == ' ' || ch == '\t':
			if field.Len() > 0 {
				fields = append(fields, field.String())
				field.Reset()
			}
		default:
			field.WriteByte(ch)
		}
	}
	if quote != 0 {
		return nil
	}
	if field.Len() > 0 {
		fields = append(fields, field.String())
	}
	return fields
}

type singboxTags []string

func (t *singboxTags) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '"' {
		var single string
		if err := json.Unmarshal(data, &single); err != nil {
			return err
		}
		*t = []string{single}
		return nil
	}
	return json.Unmarshal(data, (*[]string)(t))
}

type singboxSourceRule struct {
	Type     string            `json:"type"`
	Inbound  singboxTags       `json:"inbound"`
	Outbound singboxTags       `json:"outbound"`
	Action   string            `json:"action"`
	RuleSet  singboxTags       `json:"rule_set"`
	Rules    []json.RawMessage `json:"rules"`
}

// Match values are never copied. This list also excludes action-specific options.
var singboxConditionNames = []string{
	"ip_version", "network", "auth_user", "protocol", "client", "domain", "domain_suffix", "domain_keyword", "domain_regex",
	"geosite", "source_geoip", "geoip", "source_ip_cidr", "source_ip_is_private", "ip_cidr", "ip_is_private",
	"source_port", "source_port_range", "port", "port_range", "process_name", "process_path", "process_path_regex",
	"package_name", "package_name_regex", "user", "user_id", "clash_mode", "network_type", "network_is_expensive",
	"network_is_constrained", "interface_address", "network_interface_address", "default_interface_address", "wifi_ssid", "wifi_bssid",
	"preferred_by", "dns_server_address", "dns_search_domain", "source_mac_address", "source_hostname", "rule_set_ipcidr_match_source",
	"rule_set_ip_cidr_match_source", "rule_set_ip_cidr_accept_empty", "invert", "mode",
}

type singboxExtraction struct {
	truncated bool
	remaining int
	rulesLeft int
}

func (e *singboxExtraction) text(value string) string {
	if len(value) > protocol.MaxInventoryTextBytes {
		e.truncated = true
	}
	return inventoryText(value, protocol.MaxInventoryTextBytes)
}

func (e *singboxExtraction) listLen(n int) int {
	if n > protocol.MaxSingboxItems {
		e.truncated = true
		return protocol.MaxSingboxItems
	}
	return n
}

func (e *singboxExtraction) tags(values []string) []string {
	values = values[:e.listLen(len(values))]
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, e.text(value))
	}
	return out
}

func appendSingboxItem[T any](e *singboxExtraction, list *[]T, item T) {
	encoded, _ := json.Marshal(item) // The whitelist types contain only JSON-native values.
	if len(*list) == protocol.MaxSingboxItems || len(encoded)+1 > e.remaining {
		e.truncated = true
		return
	}
	*list = append(*list, item)
	e.remaining -= len(encoded) + 1
}

func extractSingboxStructure(ctx context.Context, documents [][]byte) (*protocol.SingboxStructure, bool, error) {
	e := singboxExtraction{remaining: protocol.MaxSingboxStructureBytes - 1024, rulesLeft: protocol.MaxSingboxItems}
	result := &protocol.SingboxStructure{
		Inbounds: []protocol.SingboxInbound{}, Outbounds: []protocol.SingboxOutbound{},
		Route: protocol.SingboxRoute{Rules: []protocol.SingboxRule{}, RuleSet: []protocol.SingboxRuleSet{}},
	}
	for _, data := range documents {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		var config *struct {
			Inbounds  []protocol.SingboxInbound  `json:"inbounds"`
			Outbounds []protocol.SingboxOutbound `json:"outbounds"`
			Endpoints []protocol.SingboxOutbound `json:"endpoints"`
			Route     struct {
				Rules   []json.RawMessage         `json:"rules"`
				Final   *string                   `json:"final"`
				RuleSet []protocol.SingboxRuleSet `json:"rule_set"`
			} `json:"route"`
		}
		if err := json.Unmarshal(data, &config); err != nil || config == nil {
			return nil, false, errors.New("invalid sing-box configuration")
		}
		for _, item := range config.Inbounds[:e.listLen(len(config.Inbounds))] {
			item.Tag, item.Type = e.text(item.Tag), e.text(item.Type)
			appendSingboxItem(&e, &result.Inbounds, item)
		}
		for _, list := range [][]protocol.SingboxOutbound{config.Outbounds, config.Endpoints} {
			for _, item := range list[:e.listLen(len(list))] {
				if item.Type == "selector" || item.Type == "urltest" {
					item.Outbounds, item.Default = e.tags(item.Outbounds), e.text(item.Default)
				} else {
					item.Outbounds, item.Default = nil, ""
				}
				item.Tag, item.Type, item.Detour = e.text(item.Tag), e.text(item.Type), e.text(item.Detour)
				appendSingboxItem(&e, &result.Outbounds, item)
			}
		}
		for _, raw := range config.Route.Rules[:e.listLen(len(config.Route.Rules))] {
			if e.rulesLeft == 0 {
				e.truncated = true
				break
			}
			rule, err := e.rule(ctx, raw, 0)
			if err != nil {
				return nil, false, err
			}
			appendSingboxItem(&e, &result.Route.Rules, rule)
		}
		if config.Route.Final != nil {
			result.Route.Final = e.text(*config.Route.Final)
		}
		for _, item := range config.Route.RuleSet[:e.listLen(len(config.Route.RuleSet))] {
			item.Tag, item.Type = e.text(item.Tag), e.text(item.Type)
			appendSingboxItem(&e, &result.Route.RuleSet, item)
		}
	}
	return result, e.truncated, ctx.Err()
}

func (e *singboxExtraction) rule(ctx context.Context, raw json.RawMessage, depth int) (protocol.SingboxRule, error) {
	var source singboxSourceRule
	if err := json.Unmarshal(raw, &source); err != nil {
		return protocol.SingboxRule{}, err
	}
	if err := ctx.Err(); err != nil {
		return protocol.SingboxRule{}, err
	}
	e.rulesLeft--
	result := protocol.SingboxRule{Inbound: e.tags(source.Inbound), Outbound: e.tags(source.Outbound), Action: e.text(source.Action), RuleSet: e.tags(source.RuleSet)}
	var conditions map[string]json.RawMessage
	if err := json.Unmarshal(raw, &conditions); err != nil || conditions == nil {
		return protocol.SingboxRule{}, errors.New("invalid sing-box route rule")
	}
	for _, name := range singboxConditionNames {
		value := bytes.TrimSpace(conditions[name])
		if len(value) == 0 || bytes.Equal(value, []byte("null")) {
			continue
		}
		count := 1
		switch value[0] {
		case '[':
			var list []json.RawMessage
			if err := json.Unmarshal(value, &list); err != nil {
				return protocol.SingboxRule{}, err
			}
			count = e.listLen(len(list))
		case '{':
			var values map[string]json.RawMessage
			if err := json.Unmarshal(value, &values); err != nil {
				return protocol.SingboxRule{}, err
			}
			count = e.listLen(len(values))
		}
		if result.Conditions == nil {
			result.Conditions = make(map[string]int)
		}
		result.Conditions[name] = count
	}
	if source.Type == "logical" {
		for _, raw := range source.Rules[:e.listLen(len(source.Rules))] {
			if depth >= 24 || e.rulesLeft == 0 {
				e.truncated = true
				break
			}
			child, err := e.rule(ctx, raw, depth+1)
			if err != nil {
				return protocol.SingboxRule{}, err
			}
			result.Rules = append(result.Rules, child)
		}
	}
	return result, nil
}
