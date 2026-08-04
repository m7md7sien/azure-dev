// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package evalcore

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// The service-target provider receives the config as JSON, not YAML, so every
// entry has to decode through both paths. Supporting only YAML made
// `azd deploy` fail on a config the CLI itself writes.
func TestEvaluatorListDecodesEntriesFromJSON(t *testing.T) {
	const payload = `[
		{"name": "builtin.task_adherence"},
		{"name": "support-quality", "threshold": 4.0},
		{"name": "pinned", "version": "3"}
	]`

	var list EvaluatorList
	require.NoError(t, json.Unmarshal([]byte(payload), &list))
	require.Len(t, list, 3)

	require.Equal(t, "builtin.task_adherence", list[0].Name)
	require.Nil(t, list[0].Threshold)

	require.Equal(t, "support-quality", list[1].Name)
	require.NotNil(t, list[1].Threshold)
	require.InDelta(t, 4.0, *list[1].Threshold, 0.0001)

	require.Equal(t, "pinned", list[2].Name)
	require.Equal(t, "3", list[2].Version)
}

// A bare string is the form the config used to accept, so it has to be refused
// with the remedy rather than a decoder's type error — through both paths,
// because a config that reaches `azd up` as JSON would otherwise report a
// different problem than the one the author can act on.
func TestEvaluatorListRejectsTheBareStringForm(t *testing.T) {
	var fromJSON EvaluatorList
	err := json.Unmarshal([]byte(`["builtin.task_adherence"]`), &fromJSON)
	require.ErrorContains(t, err, "- name: builtin.task_adherence")

	var fromYAML EvaluatorList
	err = yaml.Unmarshal([]byte("- builtin.task_adherence\n"), &fromYAML)
	require.ErrorContains(t, err, "- name: builtin.task_adherence")
}

// The JSON and YAML decoders must agree, otherwise a config behaves one way
// through the CLI and another through `azd up`.
func TestEvaluatorListJSONMatchesYAML(t *testing.T) {
	const doc = `
- name: builtin.task_adherence
- { name: support-quality, threshold: 4.0 }
`
	var fromYAML EvaluatorList
	require.NoError(t, yaml.Unmarshal([]byte(doc), &fromYAML))

	encoded, err := json.Marshal(fromYAML)
	require.NoError(t, err)

	var fromJSON EvaluatorList
	require.NoError(t, json.Unmarshal(encoded, &fromJSON))
	require.Equal(t, fromYAML, fromJSON)
}

func TestEvaluatorListRejectsEntryWithoutName(t *testing.T) {
	var list EvaluatorList
	err := json.Unmarshal([]byte(`[{"threshold": 4.0}]`), &list)
	require.Error(t, err)
	require.Contains(t, err.Error(), "name")
}
