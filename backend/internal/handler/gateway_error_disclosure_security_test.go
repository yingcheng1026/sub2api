package handler

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGatewayPublicErrorResponsesDoNotExposeInternalErrors(t *testing.T) {
	files := []string{
		"gateway_handler.go",
		"gateway_handler_chat_completions.go",
		"gateway_handler_responses.go",
		"gemini_v1beta_handler.go",
		"kiro_gateway_handler.go",
		"cursor_gateway_handler.go",
	}
	publicWriters := map[string]struct{}{
		"googleError":                  {},
		"handleStreamingAwareError":    {},
		"responsesErrorResponse":       {},
		"chatCompletionsErrorResponse": {},
	}

	for _, filename := range files {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filename, nil, 0)
		if !assert.NoError(t, err, filename) {
			continue
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || !isPublicGatewayErrorWriter(call.Fun, publicWriters) {
				return true
			}
			for _, arg := range call.Args {
				if containsErrorMethodCall(arg) {
					pos := fset.Position(arg.Pos())
					t.Errorf("%s:%d exposes err.Error() through a public gateway response", filename, pos.Line)
				}
			}
			return true
		})
	}
}

func isPublicGatewayErrorWriter(expr ast.Expr, names map[string]struct{}) bool {
	switch fn := expr.(type) {
	case *ast.Ident:
		_, ok := names[fn.Name]
		return ok
	case *ast.SelectorExpr:
		_, ok := names[fn.Sel.Name]
		return ok
	default:
		return false
	}
}

func containsErrorMethodCall(expr ast.Expr) bool {
	found := false
	ast.Inspect(expr, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if ok && selector.Sel.Name == "Error" && len(call.Args) == 0 {
			found = true
			return false
		}
		return true
	})
	return found
}
