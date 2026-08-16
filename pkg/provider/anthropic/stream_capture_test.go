package anthropic

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// Stream hands the SSE body to a goroutine that outlives Stream itself. Naming
// req inside that goroutine makes Go capture the whole core.Request by
// reference, keeping every materialized image (base64, megabytes per request)
// reachable until the stream ends. The goroutine only ever needs values copied
// out beforehand, so the request must not appear inside it.
func TestStreamGoroutineDoesNotCaptureRequest(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "anthropic.go", nil, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	var streamBody *ast.BlockStmt
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if ok && fn.Name.Name == "Stream" && fn.Recv != nil {
			streamBody = fn.Body
			return false
		}
		return true
	})
	if streamBody == nil {
		t.Fatal("Stream method not found")
	}

	var offenders []string
	ast.Inspect(streamBody, func(n ast.Node) bool {
		gostmt, ok := n.(*ast.GoStmt)
		if !ok {
			return true
		}
		ast.Inspect(gostmt, func(inner ast.Node) bool {
			sel, ok := inner.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "req" {
				offenders = append(offenders, "req."+sel.Sel.Name)
			}
			return true
		})
		return true
	})

	if len(offenders) > 0 {
		t.Fatalf("the streaming goroutine captures the request (%s): copy the value "+
			"into a local before the goroutine, or the whole request stays alive "+
			"for the duration of the stream", strings.Join(offenders, ", "))
	}
}
