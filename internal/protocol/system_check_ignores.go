package protocol

import (
	"net/netip"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	SystemCheckService     = "service"
	SystemCheckRestart     = "service_restart"
	SystemCheckCertificate = "certificate"
	SystemCheckTime        = "time"
	SystemCheckDNS         = "dns"
)

type SystemCheckKey struct {
	Kind string `json:"kind"`
	Item string `json:"item"`
}

var systemdUnit = regexp.MustCompile(`^(?:[A-Za-z0-9_.:@-]|\\x[0-9A-Fa-f]{2})+\.(service|socket|device|mount|automount|swap|target|path|timer|slice|scope)$`)

func (key SystemCheckKey) Valid() bool {
	item := key.Item
	if len(item) == 0 || len(item) > 512 || !utf8.ValidString(item) {
		return false
	}
	switch key.Kind {
	case SystemCheckService:
		return len(item) <= 255 && systemdUnit.MatchString(item)
	case SystemCheckRestart:
		return len(item) <= 255 && strings.HasSuffix(item, ".service") && systemdUnit.MatchString(item)
	case SystemCheckCertificate:
		return len(item) > 1 && strings.HasPrefix(item, "/") && !strings.HasSuffix(item, "/") &&
			strings.IndexFunc(item, unicode.IsControl) < 0
	case SystemCheckTime:
		return item == SystemCheckTime
	case SystemCheckDNS:
		addr, err := netip.ParseAddr(item)
		return err == nil && addr.Zone() == "" && addr.IsLoopback() && !addr.Is4In6() && addr.String() == item
	default:
		return false
	}
}
