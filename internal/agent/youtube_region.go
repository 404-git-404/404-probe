package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"404-probe/internal/protocol"
	"golang.org/x/net/html"
)

// This observes an anonymous web-page content region, not playback, entitlement
// or physical location. Web configuration is undocumented: unknown is deliberate
// when its structure, completeness or corroborating evidence is ambiguous.
func parseYouTube(response googleHTTPResponse) protocol.YouTubeResult {
	unknown := func(reason string) protocol.YouTubeResult {
		return protocol.YouTubeResult{Status: protocol.YouTubeUnknown, Error: protocol.GoogleServiceError{Category: reason}}
	}
	switch response.status {
	case http.StatusOK:
	case 403:
		return unknown("http_forbidden")
	case 429:
		return unknown("rate_limited")
	case 451:
		return unknown("http_restricted")
	default:
		if response.status >= 500 {
			return unknown("http_server_error")
		}
		return unknown("unrecognized_response")
	}
	u, err := url.Parse(response.finalURL)
	if err != nil || !googleHostAllowed(googleYouTube, u) || (u.Path != "/premium" && u.Path != "/premium/") {
		return unknown("unexpected_page")
	}
	media, _, err := mime.ParseMediaType(response.contentType)
	if err != nil || !strings.EqualFold(media, "text/html") || len(response.body) == 0 {
		return unknown("unrecognized_response")
	}
	z := html.NewTokenizer(bytes.NewReader(response.body))
	var scripts []string
	var corroboration []string
	var title strings.Builder
	inScript, inTitle, inHead, closedHTML := false, false, false, false
	scriptAllowed := false
	var script strings.Builder
	for {
		kind := z.Next()
		if kind == html.ErrorToken {
			if z.Err() != io.EOF {
				return unknown("response_read_error")
			}
			break
		}
		token := z.Token()
		switch kind {
		case html.StartTagToken, html.SelfClosingTagToken:
			attrs := map[string]string{}
			for _, a := range token.Attr {
				attrs[a.Key] = a.Val
			}
			switch token.Data {
			case "head":
				inHead = true
			case "title":
				inTitle = true
			case "script":
				inScript = true
				script.Reset()
				scriptAllowed = attrs["src"] == "" && (attrs["type"] == "" || attrs["type"] == "text/javascript" || attrs["type"] == "application/javascript")
				if strings.Contains(strings.ToLower(attrs["src"]), "recaptcha") || strings.Contains(strings.ToLower(attrs["src"]), "challenge-platform") {
					return unknown("challenge_page")
				}
			case "form":
				action, _ := url.Parse(attrs["action"])
				if action != nil && (action.Hostname() == "consent.youtube.com" || action.Hostname() == "consent.google.com") {
					return unknown("consent_page")
				}
			case "meta":
				if inHead && strings.EqualFold(attrs["itemprop"], "contentRegion") {
					corroboration = append(corroboration, attrs["content"])
				}
			}
			for _, key := range []string{"id", "class"} {
				v := strings.ToLower(attrs[key])
				if strings.Contains(v, "g-recaptcha") || strings.Contains(v, "h-captcha") || strings.Contains(v, "cf-challenge") {
					return unknown("challenge_page")
				}
			}
		case html.TextToken:
			if inScript {
				script.WriteString(token.Data)
			} else if inTitle {
				title.WriteString(token.Data)
			} else {
				text := strings.ToLower(token.Data)
				if strings.Contains(text, "our systems have detected unusual traffic") || strings.Contains(text, "unusual traffic from your computer network") {
					return unknown("challenge_page")
				}
			}
		case html.EndTagToken:
			switch token.Data {
			case "script":
				if scriptAllowed {
					scripts = append(scripts, script.String())
				}
				inScript = false
			case "title":
				inTitle = false
			case "head":
				inHead = false
			case "html":
				closedHTML = true
			}
		}
	}
	label := strings.ToLower(title.String())
	if strings.Contains(label, "before you continue") {
		return unknown("consent_page")
	}
	if strings.Contains(label, "verify your identity") || strings.Contains(label, "just a moment") || strings.Contains(label, "access denied") {
		return unknown("challenge_page")
	}
	if !closedHTML || inScript {
		return unknown("response_truncated")
	}
	if !strings.Contains(label, "youtube") {
		return unknown("missing_evidence")
	}
	var regions []string
	var zwieback []any
	for _, s := range scripts {
		configs, err := youtubeConfigs(s)
		if err != nil {
			// Only a script that carries ytcfg and region-related fields can make the page untrustworthy;
			// an unrelated ytcfg script that this strict reader cannot parse is simply not evidence.
			if strings.Contains(s, "ytcfg") && youtubeMentionsRegion(s) {
				return unknown("invalid_configuration")
			}
			continue
		}
		for _, config := range configs {
			if raw, ok := config["ZWIEBACK_PING_URLS"]; ok {
				zwieback = append(zwieback, raw)
			}
			if raw, ok := config["INNERTUBE_CONTEXT_GL"]; ok {
				v, ok := raw.(string)
				if !ok || !protocol.ValidCountryCode(v) {
					return unknown("invalid_region")
				}
				regions = append(regions, v)
			}
			if context, ok := config["INNERTUBE_CONTEXT"].(map[string]any); ok {
				if client, ok := context["client"].(map[string]any); ok {
					if raw, ok := client["gl"]; ok {
						v, ok := raw.(string)
						if !ok || !protocol.ValidCountryCode(v) {
							return unknown("invalid_region")
						}
						regions = append(regions, v)
					}
				}
			}
			if raw, ok := config["contentRegion"]; ok {
				v, ok := raw.(string)
				if !ok {
					return unknown("invalid_region")
				}
				corroboration = append(corroboration, v)
			}
		}
	}
	region, reason := youtubeAggregateRegion(regions, corroboration)
	if reason == "invalid_region" {
		return unknown(reason)
	}
	// Google decides per IP whether a visitor is treated as being in China. For such an IP the page's
	// own region (GL, contentRegion) is only a default, so the Google China ping URL in the page
	// configuration outranks it. Only the strictly parsed key counts; see youtubeZwiebackChina.
	if youtubeZwiebackChina(zwieback) {
		if reason != "" {
			region = ""
		}
		return youtubeChinaResult(region, response.body)
	}
	if reason == "" && region == "CN" {
		return youtubeChinaResult(region, response.body)
	}
	// A China domain somewhere in the page that the configuration did not confirm is a signal this
	// reader cannot vouch for: report it as unknown instead of claiming the visitor is not in China.
	// This byte scan can only produce unknown, never a China verdict.
	if youtubeMentionsChinaDomain(response.body) {
		return unknown("china_signal_unverified")
	}
	if reason != "" {
		return unknown(reason)
	}
	cn := false
	return protocol.YouTubeResult{Status: protocol.YouTubeNotCN, Region: region, SentToChina: &cn}
}

// The page region as the aggregate of every trusted GL and its contentRegion corroboration.
func youtubeAggregateRegion(regions, corroboration []string) (string, string) {
	if len(regions) == 0 {
		return "", "missing_evidence"
	}
	region := regions[0]
	for _, v := range append(append([]string{}, regions...), corroboration...) {
		if !protocol.ValidCountryCode(v) {
			return "", "invalid_region"
		}
		if v != region {
			return "", "region_conflict"
		}
	}
	return region, ""
}

func youtubeChinaResult(region string, body []byte) protocol.YouTubeResult {
	cn := true
	notice := bytes.Contains(body, []byte(`YouTube Premium is not available in your country"`))
	return protocol.YouTubeResult{Status: protocol.YouTubeCN, Region: region, SentToChina: &cn, PremiumNotice: &notice}
}

// True only when every top-level ZWIEBACK_PING_URLS of the strictly parsed configurations is an array
// of strings and contains at least one URL whose host is google.cn or below it.
func youtubeZwiebackChina(values []any) bool {
	if len(values) == 0 {
		return false
	}
	for _, value := range values {
		urls, ok := value.([]any)
		if !ok {
			return false
		}
		found := false
		for _, element := range urls {
			raw, ok := element.(string)
			if !ok {
				return false
			}
			if youtubeChinaURL(raw) {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func youtubeChinaURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	return host == "google.cn" || strings.HasSuffix(host, ".google.cn")
}

// google.cn as a whole label sequence: not preceded by [a-z0-9-] and not followed by [a-z0-9_-].
func youtubeMentionsChinaDomain(body []byte) bool {
	lower := bytes.ToLower(body)
	needle := []byte("google.cn")
	for offset := 0; ; {
		found := bytes.Index(lower[offset:], needle)
		if found < 0 {
			return false
		}
		found += offset
		end := found + len(needle)
		if (found == 0 || !youtubeLabelByte(lower[found-1], false)) && (end >= len(lower) || !youtubeLabelByte(lower[end], true)) {
			return true
		}
		offset = found + 1
	}
}

func youtubeLabelByte(c byte, underscore bool) bool {
	return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || underscore && c == '_'
}

// Extract only literal, top-level ytcfg.set(JSON) statements in inline script.
// Skip JS strings, templates, comments and nested/dead function bodies. Do not
// execute JavaScript or scan arbitrary occurrences of country-like strings.
func youtubeConfigs(script string) ([]map[string]any, error) {
	return youtubeConfigsAt(script, 0)
}

func youtubeConfigsAt(script string, level int) ([]map[string]any, error) {
	var configs []map[string]any
	depth := 0
	statement := true
	for i := 0; i < len(script); {
		c := script[i]
		if strings.ContainsRune(" \t\r\n\f", rune(c)) {
			i++
			continue
		}
		if c == '/' && i+1 < len(script) {
			if script[i+1] == '/' {
				end := strings.IndexByte(script[i+2:], '\n')
				if end < 0 {
					break
				}
				i += end + 3
				continue
			}
			if script[i+1] == '*' {
				end := strings.Index(script[i+2:], "*/")
				if end < 0 {
					return nil, errors.New("incomplete script comment")
				}
				i += end + 4
				continue
			}
			// Regex contents must not become config calls. Ambiguous division
			// syntax is conservatively unsupported rather than executed.
			i++
			closed, inClass := false, false
			for i < len(script) {
				if script[i] == '\\' {
					i += 2
					continue
				}
				if script[i] == '[' {
					inClass = true
				}
				if script[i] == ']' {
					inClass = false
				}
				if script[i] == '/' && !inClass {
					i++
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, errors.New("ambiguous script literal")
			}
			statement = false
			continue
		}
		if depth == 0 && statement {
			// Code after a top-level return/throw in an IIFE body is dead.
			if level > 0 && (youtubeKeyword(script[i:], "return") || youtubeKeyword(script[i:], "throw")) {
				break
			}
			// One level of a directly invoked (function name?(){...})() body is
			// scanned as top level; other function bodies stay skipped.
			if level == 0 && c == '(' {
				if bodyStart, bodyEnd, next, ok := youtubeIIFE(script, i); ok {
					nested, err := youtubeConfigsAt(script[bodyStart:bodyEnd], level+1)
					if err != nil {
						return nil, err
					}
					configs = append(configs, nested...)
					i = next
					statement = false
					continue
				}
			}
			start := i
			for _, prefix := range []string{"ytcfg", "window.ytcfg"} {
				if !strings.HasPrefix(script[start:], prefix) {
					continue
				}
				j := start + len(prefix)
				for j < len(script) && strings.ContainsRune(" \t\r\n", rune(script[j])) {
					j++
				}
				if j >= len(script) || script[j] != '.' {
					continue
				}
				j++
				for j < len(script) && strings.ContainsRune(" \t\r\n", rune(script[j])) {
					j++
				}
				if !strings.HasPrefix(script[j:], "set") {
					continue
				}
				j += 3
				for j < len(script) && strings.ContainsRune(" \t\r\n", rune(script[j])) {
					j++
				}
				if j >= len(script) || script[j] != '(' {
					continue
				}
				paren := j
				j++
				if object, end, ok := youtubeLiteralCall(script, j); ok {
					configs = append(configs, object)
					i = end + 1
					statement = false
					break
				}
				// Not one strict-JSON object (key/value form, JS literal or an
				// expression): ignore it only when it cannot carry region keys.
				end, err := youtubeBalancedEnd(script, paren)
				if err != nil {
					return nil, err
				}
				if youtubeMentionsRegion(script[paren : end+1]) {
					return nil, errors.New("non-literal region config")
				}
				i = end + 1
				statement = false
				break
			}
			if i != start {
				continue
			}
		}
		if c == '\'' || c == '"' || c == '`' {
			quote := c
			i++
			closed := false
			for i < len(script) {
				if script[i] == '\\' {
					i += 2
					continue
				}
				if script[i] == quote {
					i++
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, errors.New("incomplete script string")
			}
			statement = false
			continue
		}
		switch c {
		case '{', '[', '(':
			depth++
		case '}', ']', ')':
			if depth > 0 {
				depth--
			}
		case ';':
			if depth == 0 {
				statement = true
				i++
				continue
			}
		}
		statement = false
		i++
	}
	return configs, nil
}

// Duplicate keys and pathological nesting are ambiguous, even when encoding/json
// would otherwise silently choose the final country value.
func youtubeJSON(d *json.Decoder, depth int) (any, error) {
	if depth > 64 {
		return nil, errors.New("config nesting limit")
	}
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := t.(json.Delim); ok {
		switch delimiter {
		case '{':
			m := map[string]any{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return nil, err
				}
				k, ok := key.(string)
				if !ok {
					return nil, errors.New("invalid key")
				}
				if _, exists := m[k]; exists {
					return nil, errors.New("duplicate key")
				}
				v, err := youtubeJSON(d, depth+1)
				if err != nil {
					return nil, err
				}
				m[k] = v
			}
			_, err = d.Token()
			return m, err
		case '[':
			var values []any
			for d.More() {
				v, err := youtubeJSON(d, depth+1)
				if err != nil {
					return nil, err
				}
				values = append(values, v)
			}
			_, err = d.Token()
			return values, err
		default:
			return nil, errors.New("invalid delimiter")
		}
	}
	return t, nil
}

func youtubeLiteralCall(script string, j int) (map[string]any, int, bool) {
	k := j
	for k < len(script) && strings.ContainsRune(" \t\r\n", rune(script[k])) {
		k++
	}
	if k >= len(script) || script[k] != '{' {
		return nil, 0, false
	}
	d := json.NewDecoder(strings.NewReader(script[j:]))
	d.UseNumber()
	value, err := youtubeJSON(d, 0)
	if err != nil {
		return nil, 0, false
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, 0, false
	}
	j += int(d.InputOffset())
	for j < len(script) && strings.ContainsRune(" \t\r\n", rune(script[j])) {
		j++
	}
	if j >= len(script) || script[j] != ')' {
		return nil, 0, false
	}
	return object, j, true
}

func youtubeMentionsRegion(call string) bool {
	if strings.Contains(call, "INNERTUBE_CONTEXT") || strings.Contains(call, "contentRegion") || strings.Contains(call, `"gl"`) || strings.Contains(call, `'gl'`) {
		return true
	}
	for i := 0; ; {
		rel := strings.Index(call[i:], "gl")
		if rel < 0 {
			return false
		}
		i += rel
		k := i + 2
		for k < len(call) && strings.ContainsRune(" \t\r\n", rune(call[k])) {
			k++
		}
		if (i == 0 || !youtubeIdent(call[i-1])) && k < len(call) && call[k] == ':' {
			return true
		}
		i += 2
	}
}

func youtubeIdent(c byte) bool {
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func youtubeKeyword(s, word string) bool {
	return strings.HasPrefix(s, word) && (len(s) == len(word) || !youtubeIdent(s[len(word)]))
}

// Matching close of the bracket at open, skipping strings and comments. A bare
// slash (regex or division) is ambiguous and rejected rather than guessed.
func youtubeBalancedEnd(script string, open int) (int, error) {
	depth := 0
	for i := open; i < len(script); {
		c := script[i]
		switch {
		case c == '/' && i+1 < len(script) && script[i+1] == '/':
			end := strings.IndexByte(script[i+2:], '\n')
			if end < 0 {
				return 0, errors.New("incomplete config call")
			}
			i += end + 3
			continue
		case c == '/' && i+1 < len(script) && script[i+1] == '*':
			end := strings.Index(script[i+2:], "*/")
			if end < 0 {
				return 0, errors.New("incomplete script comment")
			}
			i += end + 4
			continue
		case c == '/':
			return 0, errors.New("ambiguous script literal")
		case c == '\'' || c == '"' || c == '`':
			i++
			closed := false
			for i < len(script) {
				if script[i] == '\\' {
					i += 2
					continue
				}
				if script[i] == c {
					i++
					closed = true
					break
				}
				i++
			}
			if !closed {
				return 0, errors.New("incomplete script string")
			}
			continue
		case c == '(' || c == '[' || c == '{':
			depth++
		case c == ')' || c == ']' || c == '}':
			depth--
			if depth == 0 {
				return i, nil
			}
		}
		i++
	}
	return 0, errors.New("incomplete config call")
}

// Recognize only (function [name]() { body })() at a statement start.
func youtubeIIFE(script string, i int) (int, int, int, bool) {
	ws := func(k int) int {
		for k < len(script) && strings.ContainsRune(" \t\r\n", rune(script[k])) {
			k++
		}
		return k
	}
	k := ws(i + 1)
	if !youtubeKeyword(script[k:], "function") {
		return 0, 0, 0, false
	}
	k = ws(k + len("function"))
	for k < len(script) && youtubeIdent(script[k]) {
		k++
	}
	k = ws(k)
	if k >= len(script) || script[k] != '(' {
		return 0, 0, 0, false
	}
	k = ws(k + 1)
	if k >= len(script) || script[k] != ')' {
		return 0, 0, 0, false
	}
	brace := ws(k + 1)
	if brace >= len(script) || script[brace] != '{' {
		return 0, 0, 0, false
	}
	end, err := youtubeBalancedEnd(script, brace)
	if err != nil {
		return 0, 0, 0, false
	}
	k = ws(end + 1)
	for _, want := range []byte{')', '(', ')'} {
		if k >= len(script) || script[k] != want {
			return 0, 0, 0, false
		}
		k = ws(k + 1)
	}
	return brace + 1, end, k, true
}
