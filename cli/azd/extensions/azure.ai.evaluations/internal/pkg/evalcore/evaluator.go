// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package evalcore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// BuiltinPrefix marks an evaluator provided by the platform. The prefix is
// stripped before the name is sent as testing_criteria[].evaluator_name.
const BuiltinPrefix = "builtin."

// EvaluatorRef references an evaluator from an eval. Every entry is a mapping,
// even one carrying nothing but a name:
//
//	evaluators:
//	  - name: builtin.task_adherence
//	  - name: support-agent-quality
//	    source: ./evaluators/support-agent-quality.json
//	    initialization_parameters:
//	      deployment_name: gpt-5.6-luna
//
// A built-in needs nothing but its name. One with a Source is the project's
// own, and is published before the eval that references it is created.
//
// A bare string is deliberately not accepted. Every other collection in the
// config is a list of named maps, so a shorthand here would be the one
// exception a reader has to learn; most references carry parameters anyway;
// and a bare string would have to mean evaluator_name, which reads as the
// criterion's own name — a different and also required field.
type EvaluatorRef struct {
	Name    string `yaml:"name" json:"name"`
	Version string `yaml:"version,omitempty" json:"version,omitempty"`
	// Source names a local rubric JSON file. Empty means the evaluator is
	// already registered, or is a built-in.
	Source string `yaml:"source,omitempty" json:"source,omitempty"`
	// InitializationParameters are passed through to the testing criterion,
	// which is where a judge deployment is named.
	InitializationParameters map[string]any `yaml:"initialization_parameters,omitempty" json:"initialization_parameters,omitempty"`
	// Threshold maps to testing_criteria[].initialization_parameters.threshold.
	Threshold *float64 `yaml:"threshold,omitempty" json:"threshold,omitempty"`
}

// IsBuiltin reports whether the reference names a platform evaluator, which
// needs no declaration and is never uploaded.
func (e EvaluatorRef) IsBuiltin() bool {
	return strings.HasPrefix(e.Name, BuiltinPrefix)
}

// APIName is the name the service expects, with the builtin prefix removed.
func (e EvaluatorRef) APIName() string {
	return strings.TrimPrefix(e.Name, BuiltinPrefix)
}

// EvaluatorList is a sequence of EvaluatorRef. Entries are mappings; the
// decoders name the remedy rather than accepting a bare string quietly,
// because a config written the old way is otherwise silently reinterpreted.
type EvaluatorList []EvaluatorRef

const bareEvaluatorRemedy = "an evaluator entry is a mapping, not a bare string: " +
	"write `- name: %s`"

func (el *EvaluatorList) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.SequenceNode {
		return fmt.Errorf("evaluators must be a sequence, got %v", value.Kind)
	}

	result := make([]EvaluatorRef, 0, len(value.Content))
	for _, node := range value.Content {
		switch node.Kind {
		case yaml.ScalarNode:
			var name string
			if err := node.Decode(&name); err != nil {
				return fmt.Errorf("decoding evaluator name: %w", err)
			}
			return fmt.Errorf(bareEvaluatorRemedy, name)
		case yaml.MappingNode:
			var ref EvaluatorRef
			if err := node.Decode(&ref); err != nil {
				return fmt.Errorf("decoding evaluator: %w", err)
			}
			if ref.Name == "" {
				return fmt.Errorf("evaluator entry is missing 'name'")
			}
			result = append(result, ref)
		default:
			return fmt.Errorf("evaluator entry must be a mapping, got %v", node.Kind)
		}
	}

	*el = result
	return nil
}

// MarshalYAML is the default sequence encoding. It exists only so the compact
// form cannot creep back in through the encoder.
func (el EvaluatorList) MarshalYAML() (any, error) {
	out := make([]any, 0, len(el))
	for _, ref := range el {
		out = append(out, ref)
	}
	return out, nil
}

// UnmarshalJSON accepts the same mapping-only form as the YAML decoder.
//
// This matters for the service-target provider: azd hands the service entry to
// the extension as JSON, so a config written as `- builtin.task_adherence`
// arrives here as a bare string and has to be refused with the same remedy the
// YAML decoder gives, not a decoder's own type error.
func (el *EvaluatorList) UnmarshalJSON(data []byte) error {
	var entries []json.RawMessage
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("evaluators must be a list: %w", err)
	}

	result := make([]EvaluatorRef, 0, len(entries))
	for _, entry := range entries {
		trimmed := bytes.TrimSpace(entry)
		if len(trimmed) > 0 && trimmed[0] == '"' {
			var name string
			if err := json.Unmarshal(trimmed, &name); err != nil {
				return fmt.Errorf("decoding evaluator name: %w", err)
			}
			return fmt.Errorf(bareEvaluatorRemedy, name)
		}

		var ref EvaluatorRef
		if err := json.Unmarshal(trimmed, &ref); err != nil {
			return fmt.Errorf("decoding evaluator: %w", err)
		}
		if ref.Name == "" {
			return fmt.Errorf("evaluator entry is missing 'name'")
		}
		result = append(result, ref)
	}

	*el = result
	return nil
}

// MarshalJSON mirrors MarshalYAML.
//
// Everything the reference carries has to survive the round trip, including
// the source and the initialization parameters: the eval fingerprint is taken
// over this encoding, so a field dropped here is a change the reconciler
// cannot see.
func (el EvaluatorList) MarshalJSON() ([]byte, error) {
	// Aliased so the element encoder does not recurse through this method.
	type ref = EvaluatorRef

	out := make([]any, 0, len(el))
	for _, r := range el {
		out = append(out, ref(r))
	}
	return json.Marshal(out)
}
