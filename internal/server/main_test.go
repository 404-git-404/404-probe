package server

import (
	"os"
	"testing"

	"404-probe/internal/protocol"
)

// Quality fixtures point catalog slots at loopback servers. The real resolvers (which only know the
// embedded catalog and reject everything else) are restored by tests that exercise them.
func fixtureQualityResolver(c protocol.QualityChoice, family string) (protocol.QualityTarget, error) {
	if c.Source == "catalog" && c.Host != "" {
		target := protocol.QualityTarget{Host: c.Host, Port: c.Port, EndpointVersion: "fixture"}
		if c.Protocol == "icmp" {
			target.Port = 0
		}
		return target, nil
	}
	return resolveQualityTarget(c, family)
}

func fixtureQualityResolverV2(c protocol.QualityChoice, family string) (protocol.QualityTarget, error) {
	if c.Source == "catalog" && c.Host != "" {
		target, err := fixtureQualityResolver(c, family)
		target.Region = c.Region
		return target, err
	}
	return resolveQualityTargetV2(c, family)
}

func TestMain(m *testing.M) {
	qualityResolver, qualityResolverV2 = fixtureQualityResolver, fixtureQualityResolverV2
	os.Exit(m.Run())
}
