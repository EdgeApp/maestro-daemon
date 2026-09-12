package daemon

// validate.go checks the field names of a command's map form against the
// generated command specs before the flow parser sees them.
//
// The parser ignores keys it does not recognise, which is right for YAML
// flows (forward compatibility) but wrong for an API: POSTing
// {"txt": "Login"} to /commands/tapOn would tap nothing, report ok and leave
// the caller none the wiser. The CLI already rejects unknown fields, so this
// is what makes the three surfaces agree.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/devicelab-dev/maestro-runner/pkg/flow"
)

// baseFields are the BaseStep keys every command accepts.
var baseFields = map[string]bool{"optional": true, "label": true, "timeout": true, "platform": true}

// nestedStepKeys are the fields of compound commands that hold sub-steps.
var nestedStepKeys = []string{"commands", "elseCommands"}

// buildStep validates the value and parses it into a step.
func buildStep(name string, value any, src string) (flow.Step, error) {
	if err := validateStepValue(name, value); err != nil {
		return nil, err
	}
	return flow.BuildStep(name, value, src)
}

// validateStepValue rejects field names the command does not have. Values
// that are not maps (a bare scalar, a list) are left to the parser.
func validateStepValue(name string, value any) *Error {
	spec, ok := CommandSpecs[name]
	if !ok {
		return nil
	}
	m, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	if !spec.FreeForm {
		var unknown []string
		for k := range m {
			if !spec.hasField(k) {
				unknown = append(unknown, k)
			}
		}
		if len(unknown) > 0 {
			sort.Strings(unknown)
			return Errorf(CodeUsage, "%s has no field %s; it takes %s (`maestro-d commands %s` lists them all)",
				name, quoteList(unknown), fieldList(spec), name)
		}
	}
	for _, key := range nestedStepKeys {
		list, ok := m[key].([]any)
		if !ok {
			continue
		}
		for i, raw := range list {
			if err := validateNestedStep(raw); err != nil {
				return Errorf(CodeUsage, "%s.%s[%d]: %s", name, key, i, err.Message)
			}
		}
	}
	return nil
}

// validateNestedStep checks one entry of a nested command list, which is a
// step in YAML data-model form: {tapOn: {...}} or a bare command name.
func validateNestedStep(raw any) *Error {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	for k, v := range m {
		if flow.IsStepType(k) {
			return validateStepValue(k, v)
		}
	}
	return nil
}

func (s CommandSpec) hasField(key string) bool {
	for _, f := range s.Fields {
		if f.Key == key {
			return true
		}
	}
	return false
}

// maxListedFields caps the suggestion list; a selector command has forty.
const maxListedFields = 12

// fieldList names a command's fields for an error message, its own first and
// the four every command shares last, truncated when there are many.
func fieldList(spec CommandSpec) string {
	var own, base []string
	for _, f := range spec.Fields {
		if baseFields[f.Key] {
			base = append(base, f.Key)
			continue
		}
		own = append(own, f.Key)
	}
	all := append(own, base...)
	if len(all) <= maxListedFields {
		return strings.Join(all, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(all[:maxListedFields], ", "), len(all)-maxListedFields)
}

func quoteList(keys []string) string {
	quoted := make([]string, 0, len(keys))
	for _, k := range keys {
		quoted = append(quoted, fmt.Sprintf("%q", k))
	}
	return strings.Join(quoted, ", ")
}
