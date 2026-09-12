package daemon

import (
	"context"
	"strings"
	"testing"
)

// A misspelled field must be a USAGE error rather than a step that silently
// drops it and reports success.
func TestValidate_UnknownFieldIsUsage(t *testing.T) {
	env := newTestEnv(t, 0)
	ctx := context.Background()
	env.attach("mock-1")

	for _, tc := range []struct {
		name  string
		value any
		want  string
	}{
		{"tapOn", map[string]any{"txt": "Login"}, `tapOn has no field "txt"`},
		{"tapOn", map[string]any{"value": "Login"}, `tapOn has no field "value"`},
		{"swipe", map[string]any{"direction": "UP", "millis": 400}, `swipe has no field "millis"`},
		{"repeat", map[string]any{"times": 2, "commands": []any{
			map[string]any{"tapOn": map[string]any{"txt": "x"}},
		}}, `repeat.commands[0]: tapOn has no field "txt"`},
	} {
		res, err := env.c.Command(ctx, "mock-1", tc.name, tc.value, true)
		if err == nil {
			t.Fatalf("%s %v: expected an error, got %+v", tc.name, tc.value, res)
		}
		if codeOf(t, err) != CodeUsage {
			t.Fatalf("%s: code = %v, want USAGE", tc.name, codeOf(t, err))
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: message = %q, want it to contain %q", tc.name, err.Error(), tc.want)
		}
	}
}

// The shapes the API is meant to accept keep working: the scalar shorthand,
// fields and modifiers together, nested steps, and the free-form map of
// defineVariables (whose keys are variable names, not fields).
func TestValidate_AcceptedShapes(t *testing.T) {
	env := newTestEnv(t, 0)
	ctx := context.Background()
	env.attach("mock-1")

	for _, tc := range []struct {
		name  string
		value any
	}{
		{"tapOn", "Login"},
		{"tapOn", map[string]any{"text": "Login", "timeout": 5000, "optional": true}},
		{"tapOn", map[string]any{"id": "submit", "index": 1}},
		{"defineVariables", map[string]any{"USER": "bob", "N": "3"}},
		{"repeat", map[string]any{"times": 2, "commands": []any{
			map[string]any{"tapOn": map[string]any{"text": "x"}},
		}}},
	} {
		res, err := env.c.Command(ctx, "mock-1", tc.name, tc.value, true)
		if err != nil || !res.OK {
			t.Fatalf("%s %v: %v (%+v)", tc.name, tc.value, err, res)
		}
	}
}

// Only a command whose value is a map of arbitrary keys may skip field
// checking. If the generator ever marks another one, its keys stop being
// validated, so the set is pinned here.
func TestValidate_FreeFormCommands(t *testing.T) {
	var free []string
	for name, spec := range CommandSpecs {
		if spec.FreeForm {
			free = append(free, name)
		}
	}
	if len(free) != 1 || free[0] != "defineVariables" {
		t.Fatalf("free-form commands = %v, want only defineVariables", free)
	}
}

// A command that declares no fields of its own still only takes the four
// base ones — it is not free-form.
func TestValidate_BaseOnlyCommandsAreChecked(t *testing.T) {
	if err := validateStepValue("waitForAnimationToEnd", map[string]any{"timeout": 500}); err != nil {
		t.Fatalf("base field rejected: %v", err)
	}
	if err := validateStepValue("waitForAnimationToEnd", map[string]any{"ms": 500}); err == nil {
		t.Fatal("expected USAGE for an unknown field on waitForAnimationToEnd")
	}
}
