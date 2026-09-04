package protocol

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	MaxOutboundSnapshotBytes = 64 << 10
	MaxOutboundSelectors     = 64
	MaxOutboundChoices       = 256
	MaxOutboundNameBytes     = 256
)

type OutboundStatus string

const (
	OutboundStatusConnected    OutboundStatus = "connected"
	OutboundStatusNotDetected  OutboundStatus = "not_detected"
	OutboundStatusAuthRequired OutboundStatus = "auth_required"
	OutboundStatusUnavailable  OutboundStatus = "unavailable"
)

type OutboundSelector struct {
	Name    string   `json:"name"`
	Current string   `json:"current"`
	Choices []string `json:"choices"`
}

type OutboundSnapshot struct {
	Available bool               `json:"available"`
	Status    OutboundStatus     `json:"status,omitempty"`
	Selectors []OutboundSelector `json:"selectors"`
}

func (s OutboundSnapshot) Validate() error {
	if s.Status != "" && s.Status != OutboundStatusConnected && s.Status != OutboundStatusNotDetected && s.Status != OutboundStatusAuthRequired && s.Status != OutboundStatusUnavailable {
		return errors.New("outbound status is invalid")
	}
	if s.Available && s.Status != "" && s.Status != OutboundStatusConnected {
		return errors.New("available snapshot must have connected status")
	}
	if !s.Available && s.Status == OutboundStatusConnected {
		return errors.New("connected snapshot must be available")
	}
	if !s.Available && len(s.Selectors) != 0 {
		return errors.New("unavailable snapshot must not contain selectors")
	}
	if len(s.Selectors) > MaxOutboundSelectors {
		return fmt.Errorf("selectors must contain at most %d entries", MaxOutboundSelectors)
	}
	seen := make(map[string]struct{}, len(s.Selectors))
	for index, selector := range s.Selectors {
		if err := validateOutboundName(selector.Name); err != nil {
			return fmt.Errorf("selector %d name: %w", index, err)
		}
		if _, exists := seen[selector.Name]; exists {
			return fmt.Errorf("selector %d name is duplicated", index)
		}
		seen[selector.Name] = struct{}{}
		if err := validateOutboundName(selector.Current); err != nil {
			return fmt.Errorf("selector %d current: %w", index, err)
		}
		if len(selector.Choices) == 0 || len(selector.Choices) > MaxOutboundChoices {
			return fmt.Errorf("selector %d choices must contain between 1 and %d entries", index, MaxOutboundChoices)
		}
		choiceSeen := make(map[string]struct{}, len(selector.Choices))
		currentFound := false
		for choiceIndex, choice := range selector.Choices {
			if err := validateOutboundName(choice); err != nil {
				return fmt.Errorf("selector %d choice %d: %w", index, choiceIndex, err)
			}
			if _, exists := choiceSeen[choice]; exists {
				return fmt.Errorf("selector %d choice %d is duplicated", index, choiceIndex)
			}
			choiceSeen[choice] = struct{}{}
			currentFound = currentFound || choice == selector.Current
		}
		if !currentFound {
			return fmt.Errorf("selector %d current is not present in choices", index)
		}
	}
	return nil
}

func validateOutboundName(value string) error {
	if strings.TrimSpace(value) != value || value == "" || !utf8.ValidString(value) || len(value) > MaxOutboundNameBytes {
		return fmt.Errorf("must be valid, non-blank UTF-8 with at most %d bytes", MaxOutboundNameBytes)
	}
	return nil
}
