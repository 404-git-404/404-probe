//go:build linux

package updater

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestPlatformServeConfirmedHandoffWiring(t *testing.T) {
	// The two-digit handoff test exercises validation/exec arguments, not this
	// daemon return path. Pin its production callback as well as its behavior.
	file, err := parser.ParseFile(token.NewFileSet(), "serve_linux.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	wired := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "platformServe" {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			ret, ok := node.(*ast.ReturnStmt)
			if !ok || len(ret.Results) != 1 {
				return true
			}
			call, ok := ret.Results[0].(*ast.CallExpr)
			if !ok || len(call.Args) != 5 {
				return true
			}
			name, ok := call.Fun.(*ast.Ident)
			if !ok || name.Name != "finishPlatformServe" {
				return true
			}
			callback, ok := call.Args[4].(*ast.Ident)
			wired = ok && callback.Name == "reexecConfirmedUpdater"
			return true
		})
	}
	if !wired {
		t.Fatal("platformServe does not return the confirmed-executable handoff")
	}
	for _, status := range []string{"succeeded", "rolled_back"} {
		t.Run(status, func(t *testing.T) {
			state := State{Status: status, SourceVersion: "v1.0.9", TargetVersion: "v1.0.10", OperationID: "0123456789abcdef0123456789abcdef"}
			engine := &Engine{state: state}
			committed := make(chan struct{})
			close(committed)
			sentinel := errors.New("reexec result")
			calls := 0
			err := finishPlatformServe(context.Background(), committed, engine, errors.New("closed listener"), func(got State) error {
				calls++
				if got != state {
					t.Fatalf("state=%+v", got)
				}
				return sentinel
			})
			if err != sentinel || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	closed := make(chan struct{})
	close(closed)
	if err := finishPlatformServe(ctx, closed, &Engine{}, errors.New("closed"), func(State) error { t.Fatal("shutdown reexec"); return nil }); err != nil {
		t.Fatal(err)
	}
	want := errors.New("accept error")
	if err := finishPlatformServe(context.Background(), make(chan struct{}), &Engine{}, want, func(State) error { t.Fatal("uncommitted reexec"); return nil }); err != want {
		t.Fatal(err)
	}
}
