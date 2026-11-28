package protocol

import "errors"

const (
	MaxLocalDNSServers = 32
	DNSNotApplicable   = "not_applicable"
	DNSTimeout         = "timeout"
	DNSRefused         = "connection_refused"
	DNSServerError     = "server_error"
	DNSNoRecord        = "no_record"
)

type LocalDNSCheck struct {
	Status  string                `json:"status"`
	Query   string                `json:"query,omitempty"`
	Servers []LocalDNSObservation `json:"servers,omitempty"`
}

type LocalDNSObservation struct {
	Address             string `json:"address"`
	Result              string `json:"result"`
	CheckedAt           int64  `json:"checked_at"`
	ConsecutiveFailures int    `json:"consecutive_failures"`
}

func (d LocalDNSCheck) validate() error {
	switch d.Status {
	case DNSNotApplicable, CheckError:
		if d.Query == "" && len(d.Servers) == 0 {
			return nil
		}
	case CheckOK:
		if !validText(d.Query, 253) || len(d.Servers) == 0 || len(d.Servers) > MaxLocalDNSServers {
			break
		}
		seen := make(map[string]bool, len(d.Servers))
		for _, server := range d.Servers {
			if !(SystemCheckKey{Kind: SystemCheckDNS, Item: server.Address}).Valid() || seen[server.Address] || server.CheckedAt <= 0 {
				return errors.New("invalid local DNS server")
			}
			seen[server.Address] = true
			switch server.Result {
			case CheckOK:
				if server.ConsecutiveFailures == 0 {
					continue
				}
			case DNSTimeout, DNSRefused, DNSServerError, DNSNoRecord:
				if server.ConsecutiveFailures == 1 || server.ConsecutiveFailures == 2 {
					continue
				}
			}
			return errors.New("invalid local DNS result")
		}
		return nil
	}
	return errors.New("invalid local DNS check")
}
