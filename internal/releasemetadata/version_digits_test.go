package releasemetadata

import "testing"

func TestTwoDigitMinimumServerAndExactAssetBinding(t *testing.T) {
	d := validDocument()
	d.Version = "v1.0.10"
	d.SchemaVersion = 2
	d.Compatibility = &Compatibility{MinServerVersion: "v1.0.10", UpgradeProtocol: 2}
	raw, err := Encode(d)
	if err != nil {
		t.Fatal(err)
	}
	d, err = Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		version string
		allowed bool
	}{{"v1.0.9", false}, {"v1.0.10", true}, {"v1.0.11", true}} {
		if Compatible(d, row.version) != row.allowed {
			t.Fatal(row)
		}
	}
	if _, err := Select(d, "v1.0.10", d.Assets[0].Name, "linux", "amd64"); err != nil {
		t.Fatal(err)
	}
	if _, err := Select(d, "v1.0.9", d.Assets[0].Name, "linux", "amd64"); err == nil {
		t.Fatal("wrong tag accepted")
	}
}
