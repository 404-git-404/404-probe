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
	for _, args := range [][]string{nil, {"licenses"}, {"version"}, {"country-code", "lookup"}, {"selector-order", "extract"}, {"--server", "https://example.test"}} {
		if err := checkPlatformCommand(args, platformsupport.Policy{DeferredUnsupported: true}); err != nil {
			t.Fatalf("basic blocked %v", args)
		}
	}
}

func TestCLIPlatformGatePrecedesServiceDispatch(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "main" {
			continue
		}
		// licenses must return without probing host files. Every existing command
		// still reaches the platform gate before its original dispatch.
		// Intentionally check statement positions: licenses must be first and the
		// platform gate second, so inserting an earlier statement requires review.
		licenses, ok := fn.Body.List[0].(*ast.IfStmt)
		if !ok || licenses.Init != nil {
			t.Fatal("missing standalone licenses dispatch")
		}
		condition, ok := licenses.Cond.(*ast.BinaryExpr)
		if !ok || condition.Op != token.LAND {
			t.Fatal("licenses dispatch must check its first argument")
		}
		match, ok := condition.Y.(*ast.BinaryExpr)
		if !ok || match.Op != token.EQL {
			t.Fatal("licenses dispatch must match an exact command")
		}
		command, ok := match.Y.(*ast.BasicLit)
		if !ok || command.Value != `"licenses"` {
			t.Fatal("only licenses may precede the platform gate")
		}
		if _, ok := licenses.Body.List[len(licenses.Body.List)-1].(*ast.ReturnStmt); !ok {
			t.Fatal("licenses falls through to host probing")
		}
		gate, ok := fn.Body.List[1].(*ast.IfStmt)
		if !ok {
			t.Fatal("gate must immediately follow licenses")
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
