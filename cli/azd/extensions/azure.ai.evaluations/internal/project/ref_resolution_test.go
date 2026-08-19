// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package project

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `$ref` means the same thing to `azd up` and to the CLI commands.
//
// Core owns the resolver but does not run it for us -- it hands each extension
// the entry with `$ref` still in it. The service target called it and this path
// did not, so an include deployed fine and then failed every `azd ai eval`
// command with `unknown key "$ref"`: one file, two meanings, decided by which
// command opened it.
func TestRefResolvesOnTheCLIPathToo(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "evaluators"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "evaluators", "quality.yaml"),
		[]byte("name: support-agent-quality\nsource: ./quality.json\n"),
		0o600))

	path := filepath.Join(dir, "azure.eval.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
datasets:
  - name: golden
    file: ./datasets/golden.jsonl

evaluators:
  - $ref: ./evaluators/quality.yaml

evals:
  - name: nightly
    dataset: golden
`), 0o600))

	cfg, err := LoadEvalConfig(path)
	require.NoError(t, err, "the include the service target resolves has to resolve here too")
	require.Len(t, cfg.Evaluators, 1)
	assert.Equal(t, "support-agent-quality", cfg.Evaluators[0].Name,
		"the referenced file's content replaces the directive")
	assert.Equal(t, "./quality.json", cfg.Evaluators[0].Source)
}

// Sibling keys overlay the loaded file, which is what lets a name live in the
// configuration while the definition it names lives beside the code it grades.
func TestRefSiblingKeysOverlayTheLoadedFile(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "evaluators"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "evaluators", "quality.yaml"),
		[]byte("name: from-the-file\nsource: ./quality.json\n"),
		0o600))

	path := filepath.Join(dir, "azure.eval.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
evaluators:
  - $ref: ./evaluators/quality.yaml
    name: from-the-configuration

evals:
  - name: nightly
`), 0o600))

	cfg, err := LoadEvalConfig(path)
	require.NoError(t, err)
	require.Len(t, cfg.Evaluators, 1)
	assert.Equal(t, "from-the-configuration", cfg.Evaluators[0].Name)
}

// A configuration with no include is handed to the decoder untouched, so its
// diagnostics keep the line numbers of the file the author actually wrote.
func TestConfigWithoutRefIsNotRoundTripped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "azure.eval.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
datasets:
  - name: golden
    fiel: ./datasets/golden.jsonl
`), 0o600))

	_, err := LoadEvalConfig(path)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "line 4",
		"a typo is reported where it was written, not where a re-marshal put it")
}
