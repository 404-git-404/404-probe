package webdomain

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/net/publicsuffix"
)

func ParsePublicOrigin(value string, allowInsecure bool) (*url.URL, error) {
	origin, err := parseOrigin(value)
	if err != nil {
		return nil, errors.New("Web public origin must be an absolute origin without credentials, path, query, or fragment")
	}
	switch origin.Scheme {
	case "https":
	case "http":
		if !allowInsecure || !LoopbackHost(origin.Hostname()) {
			return nil, errors.New("Web public origin must use HTTPS; insecure HTTP is allowed only for explicit loopback development")
		}
	default:
		return nil, errors.New("Web public origin must use HTTPS")
	}
	return origin, nil
}

func ParseRequestOrigin(value string) (*url.URL, error) {
	origin, err := parseOrigin(value)
	if err != nil || (origin.Scheme != "https" && origin.Scheme != "http") {
		return nil, errors.New("invalid request origin")
	}
	return origin, nil
}

func TargetOrigin(scheme, requestHost string) (*url.URL, error) {
	scheme = strings.ToLower(scheme)
	if scheme != "https" && scheme != "http" {
		return nil, errors.New("invalid target scheme")
	}
	host, _, err := CanonicalHostPort(requestHost, scheme)
	if err != nil {
		return nil, err
	}
	return &url.URL{Scheme: scheme, Host: host}, nil
}

func ParseSuffix(value string) (string, error) {
	if value == "" || strings.TrimSpace(value) != value || strings.HasPrefix(value, ".") || strings.Contains(value, ":") {
		return "", errors.New("domain suffix must be a registrable ASCII domain without a leading dot")
	}
	host, hostname, err := CanonicalHostPort(value, "https")
	if err != nil || host != hostname || net.ParseIP(hostname) != nil {
		return "", errors.New("domain suffix must be a registrable ASCII domain without a port")
	}
	registrable, err := publicsuffix.EffectiveTLDPlusOne(hostname)
	if err != nil || registrable != hostname {
		return "", errors.New("domain suffix must be a registrable domain, not a public suffix or subdomain")
	}
	return hostname, nil
}

func MatchesSuffix(hostname, suffix string) bool {
	hostname = strings.ToLower(hostname)
	suffix = strings.ToLower(suffix)
	return hostname == suffix || strings.HasSuffix(hostname, "."+suffix)
}

func CanonicalHostPort(value, scheme string) (string, string, error) {
	if value == "" || strings.TrimSpace(value) != value || strings.HasSuffix(value, ".") ||
		strings.ContainsAny(value, "@/?#\\%") {
		return "", "", errors.New("invalid Web request host")
	}
	for _, char := range value {
		if char > unicode.MaxASCII || unicode.IsControl(char) || unicode.IsSpace(char) {
			return "", "", errors.New("invalid Web request host")
		}
	}
	parsed, err := url.Parse("https://" + value)
	if err != nil || parsed.User != nil || parsed.Host != value || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", errors.New("invalid Web request host")
	}
	hostname := strings.ToLower(parsed.Hostname())
	if hostname == "" {
		return "", "", errors.New("invalid Web request host")
	}
	address := net.ParseIP(hostname)
	if address == nil {
		if err := validateDNSName(hostname); err != nil {
			return "", "", err
		}
	} else {
		hostname = strings.ToLower(address.String())
	}
	port := parsed.Port()
	if port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 || port != strconv.Itoa(number) {
			return "", "", errors.New("invalid Web request port")
		}
		port = strconv.Itoa(number)
		if (strings.EqualFold(scheme, "https") && port == "443") || (strings.EqualFold(scheme, "http") && port == "80") {
			port = ""
		}
	}
	host := hostname
	if address != nil && strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	if port != "" {
		host = net.JoinHostPort(hostname, port)
	}
	return host, hostname, nil
}

func LoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func parseOrigin(value string) (*url.URL, error) {
	origin, err := url.Parse(value)
	if err != nil || origin.User != nil || origin.Host == "" || origin.RawQuery != "" || origin.Fragment != "" ||
		(origin.Path != "" && origin.Path != "/") || origin.RawPath != "" || origin.Opaque != "" {
		return nil, errors.New("invalid origin")
	}
	origin.Scheme = strings.ToLower(origin.Scheme)
	host, _, err := CanonicalHostPort(origin.Host, origin.Scheme)
	if err != nil {
		return nil, err
	}
	origin.Host = host
	origin.Path = ""
	return origin, nil
}

func validateDNSName(host string) error {
	if len(host) > 253 || strings.Contains(host, "..") {
		return errors.New("invalid DNS host")
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("invalid DNS host")
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
				return errors.New("invalid DNS host")
			}
		}
	}
	return nil
}
