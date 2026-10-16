package releasemetadata

import "testing"

func TestRCMetadataRequiresRCAwareServer(t *testing.T) {
	d := validDocument()
	d.Version = "v1.0.3-rc.1"
	if Validate(d) == nil {
		t.Fatal("RC accepted schema1")
	}
	d.SchemaVersion = 2
	d.Compatibility = &Compatibility{MinServerVersion: "v1.0.3-rc.1", UpgradeProtocol: 2}
	b, e := Encode(d)
	if e != nil {
		t.Fatal(e)
	}
	d, e = Decode(b)
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range []string{"v1.0.1", "v1.0.2", "v1.0.3-beta.1"} {
		if Compatible(d, v) {
			t.Fatal("old Server accepted", v)
		}
	}
	for _, v := range []string{"v1.0.3-rc.1", "v1.0.3-rc.2", "v1.0.3"} {
		if !Compatible(d, v) {
			t.Fatal(v)
		}
	}
	d.Version = "v1.0.3"
	if Validate(d) == nil {
		t.Fatal("RC minimum leaked into Stable metadata")
	}
}
