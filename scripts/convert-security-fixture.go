//go:build ignore

// Converts the user-provided, overlapping journalctl text exports into a
// repository-safe JSON-lines fixture. It removes shell prompts, hostnames,
// process IDs and duplicate full event lines. Cursor values are fixture-only
// hashes and are never used by production collection.
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

var timestampPattern = regexp.MustCompile(`(-[0-9]{4}) (2026-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2})`)

func main() {
	if len(os.Args) != 3 {
		panic("usage: go run convert-security-fixture.go INPUT OUTPUT")
	}
	input, err := os.Open(os.Args[1])
	if err != nil {
		panic(err)
	}
	defer input.Close()
	output, err := os.Create(os.Args[2])
	if err != nil {
		panic(err)
	}
	defer output.Close()
	encoder := json.NewEncoder(output)
	seen := make(map[string]bool)
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	raw, unique := 0, 0
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, "REALITY: processed invalid connection") {
			continue
		}
		raw++
		if seen[line] {
			continue
		}
		seen[line] = true
		match := timestampPattern.FindStringSubmatch(line)
		messageAt := strings.Index(line, "inbound/")
		if len(match) != 3 || messageAt < 0 {
			panic("unrecognized event line")
		}
		at, err := time.Parse("-0700 2006-01-02 15:04:05", match[1]+" "+match[2])
		if err != nil {
			panic(err)
		}
		sum := sha256.Sum256([]byte(line))
		entry := map[string]string{"_SYSTEMD_UNIT": "sing-box.service", "__CURSOR": "fixture-" + hex.EncodeToString(sum[:12]), "__REALTIME_TIMESTAMP": fmt.Sprintf("%d", at.UnixMicro()), "MESSAGE": line[messageAt:]}
		if err := encoder.Encode(entry); err != nil {
			panic(err)
		}
		unique++
	}
	if err := scanner.Err(); err != nil {
		panic(err)
	}
	fmt.Printf("raw=%d unique=%d\n", raw, unique)
}
