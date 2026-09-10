package flow

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// upstreamStepTypeNames reads the case list of isStepType straight out of
// parser.go, so the test below fails the moment upstream adds a command that
// stepTypeOrder does not list yet.
func upstreamStepTypeNames(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "parser.go", nil, 0)
	if err != nil {
		t.Fatalf("parse parser.go: %v", err)
	}
	names := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "isStepType" {
			return true
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			cc, ok := n.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, e := range cc.List {
				if id, ok := e.(*ast.Ident); ok {
					names[id.Name] = true
				}
			}
			return true
		})
		return false
	})
	if len(names) == 0 {
		t.Fatal("found no case identifiers in isStepType")
	}
	return names
}

func TestStepTypesCoversUpstreamTable(t *testing.T) {
	// Map constant identifier → value by parsing step.go's const block.
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "step.go", nil, 0)
	if err != nil {
		t.Fatalf("parse step.go: %v", err)
	}
	constValue := map[string]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range vs.Names {
			if i < len(vs.Values) {
				if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					constValue[name.Name] = lit.Value[1 : len(lit.Value)-1]
				}
			}
		}
		return true
	})

	listed := map[string]bool{}
	for _, st := range StepTypes() {
		listed[string(st)] = true
	}
	for ident := range upstreamStepTypeNames(t) {
		val, ok := constValue[ident]
		if !ok {
			t.Errorf("isStepType case %s has no string constant in step.go", ident)
			continue
		}
		if !listed[val] {
			t.Errorf("upstream isStepType accepts %q (%s) but StepTypes() does not list it — add it to stepTypeOrder", val, ident)
		}
	}
	for _, st := range StepTypes() {
		if !isStepType(string(st)) {
			t.Errorf("StepTypes lists %q but isStepType rejects it", st)
		}
	}
}

func TestParseStepForms(t *testing.T) {
	cases := []struct {
		name string
		data string
		typ  StepType
	}{
		{"bare scalar", `waitForAnimationToEnd`, StepWaitForAnimationToEnd},
		{"scalar value", `tapOn: Login`, StepTapOn},
		{"json mapping", `{"tapOn": {"id": "login", "index": 1}}`, StepTapOn},
		{"json with base fields", `{"assertVisible": {"text": "Hi", "optional": true}}`, StepAssertVisible},
		{"json null value", `{"back": null}`, StepBack},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, err := ParseStep([]byte(c.data), "test")
			if err != nil {
				t.Fatalf("ParseStep(%q): %v", c.data, err)
			}
			if s.Type() != c.typ {
				t.Fatalf("type = %s, want %s", s.Type(), c.typ)
			}
		})
	}
	if _, err := ParseStep([]byte(`{"notACommand": 1}`), "test"); err == nil {
		t.Fatal("expected unknown step error")
	}
}

func TestBuildStep(t *testing.T) {
	s, err := BuildStep("tapOn", "Login", "cli")
	if err != nil {
		t.Fatal(err)
	}
	if tap, ok := s.(*TapOnStep); !ok || tap.Selector.Text != "Login" {
		t.Fatalf("scalar tapOn: got %#v", s)
	}
	s, err = BuildStep("tapOn", map[string]any{"id": "x", "index": 2, "optional": true}, "cli")
	if err != nil {
		t.Fatal(err)
	}
	tap := s.(*TapOnStep)
	if tap.Selector.ID != "x" || tap.Selector.Index != "2" || !tap.Optional {
		t.Fatalf("map tapOn: got %#v", tap)
	}
	s, err = BuildStep("back", nil, "cli")
	if err != nil || s.Type() != StepBack {
		t.Fatalf("bare back: %v %v", s, err)
	}
	if _, err := BuildStep("nope", nil, "cli"); err == nil {
		t.Fatal("expected error for unknown command")
	}
}
