package libbox

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// This is a reviewed public API baseline, independent of implementation details.
// Parameter names matter to generated Objective-C selectors as well as types.
func TestPublicAPIV1(t *testing.T) {
	set := token.NewFileSet()
	var signatures []string
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	printNode := func(node any) string {
		var b bytes.Buffer
		if err := format.Node(&b, set, node); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	for _, filename := range files {
		if strings.HasSuffix(filename, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(set, filename, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range file.Decls {
			switch decl := declaration.(type) {
			case *ast.FuncDecl:
				if !decl.Name.IsExported() {
					continue
				}
				name := decl.Name.Name
				if decl.Recv != nil {
					receiver := decl.Recv.List[0].Type
					if pointer, ok := receiver.(*ast.StarExpr); ok {
						receiver = pointer.X
					}
					if named, ok := receiver.(*ast.Ident); !ok || !named.IsExported() {
						continue
					}
					name = "(" + printNode(decl.Recv.List[0].Type) + ")." + name
				}
				signatures = append(signatures, name+strings.TrimPrefix(printNode(decl.Type), "func"))
			case *ast.GenDecl:
				for _, specification := range decl.Specs {
					typeSpec, ok := specification.(*ast.TypeSpec)
					if !ok || !typeSpec.Name.IsExported() {
						continue
					}
					if structure, ok := typeSpec.Type.(*ast.StructType); ok {
						// Engine's private synchronization state is not part of the API.
						fields := []*ast.Field{}
						for _, field := range structure.Fields.List {
							for _, name := range field.Names {
								if name.IsExported() {
									fields = append(fields, field)
									break
								}
							}
						}
						structure.Fields.List = fields
					}
					signatures = append(signatures, "type "+typeSpec.Name.Name+" "+printNode(typeSpec.Type))
				}
			}
		}
	}
	sort.Strings(signatures)
	actual := make(map[string]bool)
	for _, signature := range signatures {
		actual[signature] = true
	}
	readBaseline := func(filename string) []string {
		data, err := os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		var entries []string
		for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
			if line == "" {
				continue
			}
			if strings.HasPrefix(line, "\t") || line == "}" {
				entries[len(entries)-1] += "\n" + line
			} else {
				entries = append(entries, line)
			}
		}
		return entries
	}
	base := readBaseline("testdata/api-v1.txt")
	for _, signature := range base {
		if !actual[signature] {
			t.Fatalf("removed or changed stable API: %s", signature)
		}
		delete(actual, signature)
	}
	if len(actual) != 0 {
		if os.Getenv("UPDATE_API") != "1" {
			t.Fatalf("unreviewed additive API: %v", actual)
		}
		for signature := range actual {
			base = append(base, signature)
		}
		sort.Strings(base)
		if err := os.WriteFile("testdata/api-v1.txt", []byte(strings.Join(base, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUpstreamImportsStayInAdapter(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path == "dist" || path == ".git" || path == "tools" || path == "_build" || path == "build" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imported := range file.Imports {
			name, _ := strconv.Unquote(imported.Path.Value)
			if strings.Contains(name, "sing-box/experimental/libbox") {
				t.Errorf("%s depends on upstream libbox", path)
			}
			if strings.HasPrefix(name, "github.com/sagernet/") && !strings.HasPrefix(filepath.ToSlash(path), "internal/singbox/") {
				t.Errorf("upstream import escaped adapter: %s -> %s", path, name)
			}
			if name == "encoding/json" && !strings.HasPrefix(filepath.ToSlash(path), "internal/singbox/") {
				t.Errorf("JSON codec outside native core configuration adapter: %s", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestOnlyProtobufCExports(t *testing.T) {
	data, err := os.ReadFile("cmd/libboxc/main.go")
	if err != nil {
		t.Fatal(err)
	}
	matches := regexp.MustCompile("(?m)^//export (libbox_[a-z_]+)$").FindAllStringSubmatch(string(data), -1)
	var names []string
	for _, match := range matches {
		names = append(names, match[1])
	}
	sort.Strings(names)
	want := []string{"libbox_free_buffer", "libbox_session_call", "libbox_session_free", "libbox_session_new"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("unexpected C ABI surface: %v", names)
	}
}
