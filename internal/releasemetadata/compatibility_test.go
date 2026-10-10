package releasemetadata

import (
	"bytes"
	"testing"
)

func TestCompatibilitySchemaIsolation(t *testing.T) {
	document := validDocument()
	document.Version = "v1.0.2-beta.1"
	document.SchemaVersion = 2
	document.Compatibility = &Compatibility{MinServerVersion: "v1.0.1", UpgradeProtocol: 2}
	data, err := Encode(document)
	if err != nil {
		t.Fatal(err)
	}
	if !Compatible(document, "v1.0.1") || Compatible(document, "v1.0.0") || Compatible(document, "v1.0.1-beta.2") {
		t.Fatal("minimum Server bypass")
	}
	for _, data := range [][]byte{bytes.Replace(data, []byte(`"upgrade_protocol": 2`), []byte(`"upgrade_protocol": 3`), 1), bytes.Replace(data, []byte(`"schema_version": 2`), []byte(`"schema_version": 1`), 1), bytes.Replace(data, []byte(`"min_server_version"`), []byte(`"unknown"`), 1)} {
		if _, err := Decode(data); err == nil {
			t.Fatalf("accepted %s", data)
		}
	}
}
