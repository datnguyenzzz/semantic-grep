package splitter

// parse Go AST using standard go/ast and go/parser, completely self-contained

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
)

func parseGoFile(filePath string) ([]Chunk, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filePath, content, parser.ParseComments)
	if err != nil {
		// Fallback to whole file on syntax/parse errors
		lines := strings.Split(string(content), "\n")
		return []Chunk{{
			FilePath:  filePath,
			Content:   string(content),
			StartLine: 1,
			EndLine:   len(lines),
		}}, nil
	}

	var chunks []Chunk

	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl: // functions and methods
			pos := d.Pos()
			if d.Doc != nil {
				pos = d.Doc.Pos()
			}
			start := fset.Position(pos)
			end := fset.Position(d.End())
			text := string(content[pos-1 : d.End()-1])
			chunks = append(chunks, Chunk{
				FilePath:  filePath,
				Content:   text,
				StartLine: start.Line,
				EndLine:   end.Line,
			})
		case *ast.GenDecl: // type, var, const blocks
			switch d.Tok {
			case token.TYPE:
				for _, spec := range d.Specs {
					if ts, ok := spec.(*ast.TypeSpec); ok {
						start := fset.Position(ts.Pos())
						end := fset.Position(ts.End())

						pos := ts.Pos()
						if ts.Doc != nil {
							pos = ts.Doc.Pos()
						}

						text := string(content[pos-1 : ts.End()-1])
						chunks = append(chunks, Chunk{
							FilePath:  filePath,
							Content:   text,
							StartLine: start.Line,
							EndLine:   end.Line,
						})
					}
				}
			case token.CONST, token.VAR:
				start := fset.Position(d.Pos())
				end := fset.Position(d.End())
				text := string(content[d.Pos()-1 : d.End()-1])
				chunks = append(chunks, Chunk{
					FilePath:  filePath,
					Content:   text,
					StartLine: start.Line,
					EndLine:   end.Line,
				})
			}
		}
	}

	// Fallback if no matching declarations
	if len(chunks) == 0 {
		lines := strings.Split(string(content), "\n")
		chunks = append(chunks, Chunk{
			FilePath:  filePath,
			Content:   string(content),
			StartLine: 1,
			EndLine:   len(lines),
		})
	}

	return chunks, nil
}
