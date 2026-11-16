package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"testing"
)

func TestPublicEgressCapabilityStoreOrder(t *testing.T) {
	// Check the order in sendReport directly. A runtime test cannot reliably
	// schedule a worker between two consecutive atomic stores. On rollback to
	// v1.2.0 the sites flag must be cleared before job support is published.
	source, err := parser.ParseFile(token.NewFileSet(), "agent.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"publicEgressSitesSupported", "publicEgressSupported"}
	var got []string
	for _, decl := range source.Decls {
		method, ok := decl.(*ast.FuncDecl)
		if !ok || method.Recv == nil || method.Name.Name != "sendReport" {
			continue
		}
		for _, stmt := range method.Body.List {
			expr, ok := stmt.(*ast.ExprStmt)
			if !ok {
				continue
			}
			call, ok := expr.X.(*ast.CallExpr)
			if !ok {
				continue
			}
			store, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || store.Sel.Name != "Store" {
				continue
			}
			field, ok := store.X.(*ast.SelectorExpr)
			if ok && slices.Contains(want, field.Sel.Name) {
				got = append(got, field.Sel.Name)
			}
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("sendReport capability store order = %v, want %v", got, want)
	}
}
