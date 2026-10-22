package storage

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The guard behind qualityTargetFromPayload: the stored payload of a user target has no id, so nothing but that helper may turn a
// payload into a protocol.QualityTarget (D1, M1, M2 of R07 stage E were three places that forgot the id). It works on the syntax
// tree and follows the target through every way of declaring it, and through both ways of decoding:
//
//	var t protocol.QualityTarget           t := protocol.QualityTarget{}      t := &protocol.QualityTarget{}
//	t := new(protocol.QualityTarget)       var t = protocol.QualityTarget{}    a struct field of that type (out.Target)
//	json.Unmarshal(b, &t) / (b, t)         json.NewDecoder(r).Decode(&t) / (t)
//
// The schema 31 migration only reads the payload's source and never needs an id; it is the one exception.
var storedTargetDecoders = map[string]bool{"network_quality_target_payload.go": true, "network_quality_migration31.go": true}

func isQualityTargetType(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.SelectorExpr:
		pkg, ok := x.X.(*ast.Ident)
		return ok && pkg.Name == "protocol" && x.Sel.Name == "QualityTarget"
	case *ast.StarExpr:
		return isQualityTargetType(x.X)
	}
	return false
}

// isQualityTargetValue is true for T{}, &T{} and new(T).
func isQualityTargetValue(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.CompositeLit:
		return x.Type != nil && isQualityTargetType(x.Type)
	case *ast.UnaryExpr:
		return x.Op == token.AND && isQualityTargetValue(x.X)
	case *ast.CallExpr:
		fn, ok := x.Fun.(*ast.Ident)
		return ok && fn.Name == "new" && len(x.Args) == 1 && isQualityTargetType(x.Args[0])
	}
	return false
}

// storedTargetDecodeViolations lists "file:line" of every decode into a protocol.QualityTarget in the given sources.
func storedTargetDecodeViolations(fset *token.FileSet, files map[string]*ast.File) []string {
	fields := map[string]bool{} // struct fields typed protocol.QualityTarget, in any file of the package
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			if st, ok := n.(*ast.StructType); ok {
				for _, f := range st.Fields.List {
					if isQualityTargetType(f.Type) {
						for _, name := range f.Names {
							fields[name.Name] = true
						}
					}
				}
			}
			return true
		})
	}
	var out []string
	for name, file := range files {
		if storedTargetDecoders[name] {
			continue
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			locals := map[string]bool{}
			ast.Inspect(fn, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.ValueSpec:
					if x.Type != nil && isQualityTargetType(x.Type) {
						for _, id := range x.Names {
							locals[id.Name] = true
						}
					}
					for i, v := range x.Values {
						if i < len(x.Names) && isQualityTargetValue(v) {
							locals[x.Names[i].Name] = true
						}
					}
				case *ast.AssignStmt:
					for i, v := range x.Rhs {
						if id, ok := x.Lhs[min(i, len(x.Lhs)-1)].(*ast.Ident); ok && isQualityTargetValue(v) {
							locals[id.Name] = true
						}
					}
				case *ast.Field: // parameters and results
					if isQualityTargetType(x.Type) {
						for _, id := range x.Names {
							locals[id.Name] = true
						}
					}
				}
				return true
			})
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				var dest ast.Expr
				switch {
				case sel.Sel.Name == "Unmarshal" && len(call.Args) == 2:
					if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "json" {
						dest = call.Args[1]
					}
				case sel.Sel.Name == "Decode" && len(call.Args) == 1:
					dest = call.Args[0]
				}
				if dest == nil {
					return true
				}
				if u, ok := dest.(*ast.UnaryExpr); ok && u.Op == token.AND {
					dest = u.X
				}
				hit := false
				switch d := dest.(type) {
				case *ast.Ident:
					hit = locals[d.Name]
				case *ast.SelectorExpr:
					hit = fields[d.Sel.Name]
				}
				if hit {
					out = append(out, fset.Position(call.Pos()).String())
				}
				return true
			})
		}
	}
	sort.Strings(out)
	return out
}

func parseGuardSources(t *testing.T, sources map[string]string) (*token.FileSet, map[string]*ast.File) {
	t.Helper()
	fset, files := token.NewFileSet(), map[string]*ast.File{}
	for name, src := range sources {
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		files[name] = f
	}
	return fset, files
}

func TestStoredTargetPayloadsAreOnlyDecodedByTheHelper(t *testing.T) {
	matches, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset, files := token.NewFileSet(), map[string]*ast.File{}
	for _, name := range matches {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = f
	}
	if len(files) < 20 {
		t.Fatalf("only %d storage files parsed", len(files))
	}
	for _, v := range storedTargetDecodeViolations(fset, files) {
		t.Errorf("%s decodes a stored target payload by itself; use qualityTargetFromPayload so the id is restored", v)
	}
}

// The guard has to catch every way of writing the decode, or it only protects the way it was written for.
func TestTheStoredTargetGuardCatchesEveryWayOfDecoding(t *testing.T) {
	const head = "package storage\nimport (\"encoding/json\"; \"strings\"; \"404-probe/internal/protocol\")\nvar _ = strings.NewReader\nvar _ = json.Marshal\nvar _ protocol.QualityTarget\n"
	bad := map[string]string{
		"var and Unmarshal":      "func f(b []byte) { var t protocol.QualityTarget; json.Unmarshal(b, &t) }",
		"short declaration":      "func f(b []byte) { target := protocol.QualityTarget{}; json.Unmarshal(b, &target) }",
		"short declaration ptr":  "func f(b []byte) { target := &protocol.QualityTarget{}; json.Unmarshal(b, target) }",
		"new":                    "func f(b []byte) { target := new(protocol.QualityTarget); json.Unmarshal(b, target) }",
		"var with value":         "func f(b []byte) { var target = protocol.QualityTarget{}; _ = json.Unmarshal(b, &target) }",
		"Decoder.Decode":         "func f(r *strings.Reader) { var t protocol.QualityTarget; json.NewDecoder(r).Decode(&t) }",
		"Decoder.Decode short":   "func f(r *strings.Reader) { t := protocol.QualityTarget{}; dec := json.NewDecoder(r); _ = dec.Decode(&t) }",
		"Decoder.Decode pointer": "func f(r *strings.Reader) { t := &protocol.QualityTarget{}; json.NewDecoder(r).Decode(t) }",
		"struct field":           "type view struct{ Target protocol.QualityTarget }\nfunc f(b []byte, out *view) { json.Unmarshal(b, &out.Target) }",
		"struct field decoder":   "type view struct{ Target protocol.QualityTarget }\nfunc f(r *strings.Reader, out *view) { json.NewDecoder(r).Decode(&out.Target) }",
		"parameter":              "func f(b []byte, t *protocol.QualityTarget) { json.Unmarshal(b, t) }",
	}
	for name, body := range bad {
		fset, files := parseGuardSources(t, map[string]string{"x.go": head + body})
		if got := storedTargetDecodeViolations(fset, files); len(got) != 1 {
			t.Errorf("%s: %d violations (%v), want 1", name, len(got), got)
		}
	}
	good := map[string]string{
		"the helper":             "func f(id, payload string) { _, _ = qualityTargetFromPayload(id, payload) }",
		"another type":           "func f(b []byte) { var v struct{ A int }; json.Unmarshal(b, &v); w := map[string]any{}; json.Unmarshal(b, &w) }",
		"another decoder":        "func f(r *strings.Reader) { var v []string; json.NewDecoder(r).Decode(&v) }",
		"a target that is built": "func f() protocol.QualityTarget { t := protocol.QualityTarget{}; t.ID = \"x\"; return t }",
	}
	for name, body := range good {
		fset, files := parseGuardSources(t, map[string]string{"x.go": head + body})
		if got := storedTargetDecodeViolations(fset, files); len(got) != 0 {
			t.Errorf("%s: %v", name, got)
		}
	}
	// The migration that only reads the source is the one allowed exception.
	fset, files := parseGuardSources(t, map[string]string{"network_quality_migration31.go": head + bad["var and Unmarshal"]})
	if got := storedTargetDecodeViolations(fset, files); len(got) != 0 {
		t.Errorf("the schema 31 migration is allowed: %v", got)
	}
}
