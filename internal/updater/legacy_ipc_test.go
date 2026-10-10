package updater

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strconv"
	"testing"
)

// Derive JSON decoder types from the exact historical IPC source, not a
// hand-written approximation. Neither historical Response has custom decoding.
func historicalIPCType(t *testing.T, tag, checksum string) reflect.Type {
	t.Helper()
	data, err := os.ReadFile("testdata/ipc-" + tag + ".go.txt")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != checksum {
		t.Fatal("historical IPC source changed")
	}
	tree, err := parser.ParseFile(token.NewFileSet(), "ipc.go", data, 0)
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]*ast.StructType{}
	for _, declaration := range tree.Decls {
		if declaration, ok := declaration.(*ast.GenDecl); ok {
			for _, spec := range declaration.Specs {
				if spec, ok := spec.(*ast.TypeSpec); ok {
					if structure, ok := spec.Type.(*ast.StructType); ok {
						types[spec.Name.Name] = structure
					}
				}
			}
		}
	}
	var decodeType func(string) reflect.Type
	decodeType = func(name string) reflect.Type {
		switch name {
		case "string":
			return reflect.TypeOf("")
		case "bool":
			return reflect.TypeOf(false)
		case "int64":
			return reflect.TypeOf(int64(0))
		}
		structure := types[name]
		if structure == nil {
			t.Fatal("unsupported historical type", name)
		}
		fields := []reflect.StructField{}
		for _, field := range structure.Fields.List {
			ident, ok := field.Type.(*ast.Ident)
			if !ok || len(field.Names) != 1 || field.Tag == nil {
				t.Fatal("unsupported historical field")
			}
			tag, err := strconv.Unquote(field.Tag.Value)
			if err != nil {
				t.Fatal(err)
			}
			fields = append(fields, reflect.StructField{Name: field.Names[0].Name, Type: decodeType(ident.Name), Tag: reflect.StructTag(tag)})
		}
		return reflect.StructOf(fields)
	}
	return decodeType("Response")
}

func TestLegacyIPCResponseUsesActualHistoricalStrictDecoders(t *testing.T) {
	for _, old := range []struct{ tag, hash string }{{"v0.9.3", "3ec6d520e3bfee78d4a0dadad45b8b9be56dd47dea0ea2b8f45c35672e6c0f77"}, {"v1.0.0", "5388285220729552828e424311220660d8d0734576ee7f788b659d9c6357f3c6"}} {
		typ := historicalIPCType(t, old.tag, old.hash)
		for _, status := range []string{"claimed", "downloading", "verifying", "staging", "installing", "restarting", "health_check", "succeeded", "failed", "rolled_back"} {
			data, err := json.Marshal(Response{Accepted: true, State: State{SourceVersion: "v0.9.3", OperationID: "0123456789abcdef0123456789abcdef", TargetVersion: "v1.0.1", ReleaseCommit: "0123456789abcdef0123456789abcdef01234567", Status: status}})
			if err != nil {
				t.Fatal(err)
			}
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(reflect.New(typ).Interface()); err != nil {
				t.Fatalf("%s %s: %s: %v", old.tag, status, data, err)
			}
			if bytes.Contains(data, []byte(`"capabilities"`)) {
				t.Fatal("v0.9.3 incompatible empty capabilities")
			}
		}
		v2, _ := json.Marshal(Response{Accepted: true, Capabilities: UpdaterCapabilities{UpgradeV2: true}})
		decoder := json.NewDecoder(bytes.NewReader(v2))
		decoder.DisallowUnknownFields()
		if decoder.Decode(reflect.New(typ).Interface()) == nil {
			t.Fatal("old structure accepted V2 capability")
		}
		if old.tag == "v1.0.0" {
			data, _ := json.Marshal(Response{Accepted: true, Capabilities: UpdaterCapabilities{RemoteRemoval: true}})
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(reflect.New(typ).Interface()); err != nil {
				t.Fatal(err)
			}
		}
	}
}
