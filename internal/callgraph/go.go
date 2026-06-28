package callgraph

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
)

func parseGoFile(path string, fset *token.FileSet, nodes map[string]*Node, edges *[]Edge) error {
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return err
	}

	ast.Inspect(f, func(n ast.Node) bool {
		fnDecl, ok := n.(*ast.FuncDecl)
		if !ok {
			return true
		}

		start := fset.Position(fnDecl.Pos()).Line
		end := fset.Position(fnDecl.End()).Line

		funcName := fnDecl.Name.Name
		if fnDecl.Recv != nil && len(fnDecl.Recv.List) > 0 {
			recvType := fnDecl.Recv.List[0].Type
			var typeStr string
			switch t := recvType.(type) {
			case *ast.StarExpr:
				if ident, ok := t.X.(*ast.Ident); ok {
					typeStr = "*" + ident.Name
				}
			case *ast.Ident:
				typeStr = t.Name
			}
			if typeStr != "" {
				funcName = fmt.Sprintf("(%s).%s", typeStr, funcName)
			}
		}

		nodes[funcName] = &Node{
			Name:      funcName,
			FilePath:  path,
			StartLine: start,
			EndLine:   end,
		}

		if fnDecl.Body != nil {
			ast.Inspect(fnDecl.Body, func(bodyNode ast.Node) bool {
				call, ok := bodyNode.(*ast.CallExpr)
				if !ok {
					return true
				}

				var calleeName string
				switch fun := call.Fun.(type) {
				case *ast.Ident:
					calleeName = fun.Name
				case *ast.SelectorExpr:
					calleeName = fun.Sel.Name
				}

				if calleeName != "" {
					*edges = append(*edges, Edge{
						Caller: funcName,
						Callee: calleeName,
					})
				}
				return true
			})
		}
		return true
	})

	return nil
}
