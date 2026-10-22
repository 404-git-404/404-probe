package storage

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The guard behind qualityTargetFromPayload: the stored payload of a user target has no id, so nothing but that helper may turn a
// payload into a protocol.QualityTarget (D1, M1, M2 of R07 stage E were three places that forgot the id).
//
// THIS IS A BEST-EFFORT CHECK. It reads the syntax tree, resolves the package names from the imports (so `import p ".../protocol"`
// and `import js "encoding/json"` count), follows a QualityTarget through the usual ways of holding one, and flags a
// json.Unmarshal or Decoder.Decode into it:
//
//	var t protocol.QualityTarget      t := protocol.QualityTarget{}   t := &protocol.QualityTarget{}   t := new(protocol.QualityTarget)
//	var t = protocol.QualityTarget{}  parameters and results          a struct field of that type (out.Target)
//	dst := &t   c := t   (chains of them)                              dst := &out.Target
//	json.Unmarshal(b, &t) / (b, t)    json.NewDecoder(r).Decode(&t) / (t)
//
// It cannot see everything: it does not follow a target through a function call, an interface, reflection, a map or a slice, nor
// a local name that shadows an import. A new way of writing the decode will get past it. The real guarantee is the one function
// everybody calls and code review; this test only catches the common ways of forgetting it again. The schema 31 migration only
// reads the payload's source and never needs an id; it is the one exception.
var storedTargetDecoders = map[string]bool{"network_quality_target_payload.go": true, "network_quality_migration31.go": true}

const (
	guardProtocolPath = "404-probe/internal/protocol"
	guardJSONPath     = "encoding/json"
)

// guardNames are the names a file gives to the packages of interest.
type guardNames struct{ protocol, json map[string]bool }

func importNames(file *ast.File) guardNames {
	n := guardNames{protocol: map[string]bool{}, json: map[string]bool{}}
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		name := path[strings.LastIndex(path, "/")+1:]
		if spec.Name != nil {
			name = spec.Name.Name
		}
		switch path {
		case guardProtocolPath:
			n.protocol[name] = true
		case guardJSONPath:
			n.json[name] = true
		}
	}
	return n
}

func (n guardNames) isTargetType(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.SelectorExpr:
		pkg, ok := x.X.(*ast.Ident)
		return ok && n.protocol[pkg.Name] && x.Sel.Name == "QualityTarget"
	case *ast.StarExpr:
		return n.isTargetType(x.X)
	}
	return false
}

// isTargetValue is true for T{}, &T{} and new(T).
func (n guardNames) isTargetValue(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.CompositeLit:
		return x.Type != nil && n.isTargetType(x.Type)
	case *ast.UnaryExpr:
		return x.Op == token.AND && n.isTargetValue(x.X)
	case *ast.CallExpr:
		fn, ok := x.Fun.(*ast.Ident)
		return ok && fn.Name == "new" && len(x.Args) == 1 && n.isTargetType(x.Args[0])
	}
	return false
}

// storedTargetDecodeViolations lists "file:line" of every decode into a protocol.QualityTarget in the given sources.
func storedTargetDecodeViolations(fset *token.FileSet, files map[string]*ast.File) []string {
	// Struct fields typed protocol.QualityTarget, in any file of the package.
	fields := map[string]bool{}
	for _, file := range files {
		names := importNames(file)
		ast.Inspect(file, func(n ast.Node) bool {
			if st, ok := n.(*ast.StructType); ok {
				for _, f := range st.Fields.List {
					if names.isTargetType(f.Type) {
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
	for fileName, file := range files {
		if storedTargetDecoders[fileName] {
			continue
		}
		names := importNames(file)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			locals := map[string]bool{}
			// holds is true when the expression is (a pointer to, or a copy of) something that holds a target.
			var holds func(e ast.Expr) bool
			holds = func(e ast.Expr) bool {
				switch x := e.(type) {
				case *ast.Ident:
					return locals[x.Name]
				case *ast.SelectorExpr:
					return fields[x.Sel.Name]
				case *ast.UnaryExpr:
					return x.Op == token.AND && holds(x.X)
				case *ast.ParenExpr:
					return holds(x.X)
				}
				return names.isTargetValue(e)
			}
			// Declarations first, then the aliases of what is already known, until nothing new turns up.
			for changed := true; changed; {
				changed = false
				add := func(id *ast.Ident) {
					if id != nil && id.Name != "_" && !locals[id.Name] {
						locals[id.Name] = true
						changed = true
					}
				}
				ast.Inspect(fn, func(n ast.Node) bool {
					switch x := n.(type) {
					case *ast.ValueSpec:
						if x.Type != nil && names.isTargetType(x.Type) {
							for _, id := range x.Names {
								add(id)
							}
						}
						for i, v := range x.Values {
							if i < len(x.Names) && holds(v) {
								add(x.Names[i])
							}
						}
					case *ast.AssignStmt:
						for i, v := range x.Rhs {
							if id, ok := x.Lhs[min(i, len(x.Lhs)-1)].(*ast.Ident); ok && holds(v) {
								add(id)
							}
						}
					case *ast.Field: // parameters and results
						if names.isTargetType(x.Type) {
							for _, id := range x.Names {
								add(id)
							}
						}
					}
					return true
				})
			}
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
					if pkg, ok := sel.X.(*ast.Ident); ok && names.json[pkg.Name] {
						dest = call.Args[1]
					}
				case sel.Sel.Name == "Decode" && len(call.Args) == 1:
					dest = call.Args[0]
				}
				if dest != nil && holds(dest) {
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

// The guard has to catch the ways of writing the decode that it claims to, or it only protects the way it was written for.
// (The ways it does not claim to see are listed in its comment; this test does not try to prove them.)
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
		"pointer alias":          "func f(b []byte) { t := protocol.QualityTarget{}; dst := &t; json.Unmarshal(b, dst) }",
	}
	for name, body := range bad {
		fset, files := parseGuardSources(t, map[string]string{"x.go": head + body})
		if got := storedTargetDecodeViolations(fset, files); len(got) != 1 {
			t.Errorf("%s: %d violations (%v), want 1", name, len(got), got)
		}
	}
	// Imports under another name, and the target reached through another variable.
	const aliasHead = "package storage\nimport (js \"encoding/json\"; p \"404-probe/internal/protocol\"; \"strings\")\nvar _ = strings.NewReader\nvar _ = js.Marshal\nvar _ p.QualityTarget\n"
	badAliased := map[string]string{
		"import alias":         "func f(b []byte) { t := p.QualityTarget{}; js.Unmarshal(b, &t) }",
		"import alias decoder": "func f(r *strings.Reader) { var t p.QualityTarget; js.NewDecoder(r).Decode(&t) }",
		"import alias pointer": "func f(b []byte) { t := p.QualityTarget{}; dst := &t; js.Unmarshal(b, dst) }",
		"pointer alias var":    "func f(b []byte) { var t p.QualityTarget; var dst = &t; js.Unmarshal(b, dst) }",
		"copy":                 "func f(b []byte) { t := p.QualityTarget{}; c := t; js.Unmarshal(b, &c) }",
		"alias chain":          "func f(b []byte) { t := p.QualityTarget{}; a := &t; c := a; js.Unmarshal(b, c) }",
		"alias of a field":     "type view struct{ Target p.QualityTarget }\nfunc f(b []byte, out *view) { dst := &out.Target; js.Unmarshal(b, dst) }",
	}
	for name, body := range badAliased {
		fset, files := parseGuardSources(t, map[string]string{"x.go": aliasHead + body})
		if got := storedTargetDecodeViolations(fset, files); len(got) != 1 {
			t.Errorf("%s: %d violations (%v), want 1", name, len(got), got)
		}
	}
	goodAliased := map[string]string{
		"another type behind the alias": "func f(b []byte) { var v p.QualityConfig; js.Unmarshal(b, &v); w := &v; js.Unmarshal(b, w) }",
		"the helper":                    "func f(id, payload string) { _, _ = qualityTargetFromPayload(id, payload) }",
	}
	for name, body := range goodAliased {
		fset, files := parseGuardSources(t, map[string]string{"x.go": aliasHead + body})
		if got := storedTargetDecodeViolations(fset, files); len(got) != 0 {
			t.Errorf("%s: %v", name, got)
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
