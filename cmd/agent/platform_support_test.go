package main

import (
	"404-probe/internal/platformsupport"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestAlpineCommandGate(t *testing.T) {
	for _, args := range [][]string{{"updater"}, {"updater", "removal-worker"}, {"updater", "removal-finalize"}, {"security-collect"}} {
		if checkPlatformCommand(args, platformsupport.Policy{DeferredUnsupported: true}) == nil {
			t.Fatalf("allowed %v", args)
		}
		if err := checkPlatformCommand(args, platformsupport.Policy{}); err != nil {
			t.Fatalf("changed nonAlpine %v", args)
		}
	}
	for _, args := range [][]string{nil, {"version"}, {"country-code", "lookup"}, {"selector-order", "extract"}, {"--server", "https://example.test"}} {
		if err := checkPlatformCommand(args, platformsupport.Policy{DeferredUnsupported: true}); err != nil {
			t.Fatalf("basic blocked %v", args)
		}
	}
}

func TestCLIPlatformGatePrecedesAllDispatch(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "main" {
			continue
		}
		gate, ok := fn.Body.List[0].(*ast.IfStmt)
		if !ok {
			t.Fatal("gate not first")
		}
		a, ok := gate.Init.(*ast.AssignStmt)
		if !ok {
			t.Fatal("missing gate")
		}
		call, ok := a.Rhs[0].(*ast.CallExpr)
		if !ok {
			t.Fatal("missing call")
		}
		name, ok := call.Fun.(*ast.Ident)
		if !ok || name.Name != "checkPlatformCommand" {
			t.Fatal("dispatch precedes gate")
		}
		return
	}
	t.Fatal("main missing")
}
