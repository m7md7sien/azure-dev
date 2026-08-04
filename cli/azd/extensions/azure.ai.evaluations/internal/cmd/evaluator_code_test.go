// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package cmd

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"azureaieval/internal/pkg/eval_api"
	"azureaieval/internal/pkg/evalcore"
	"azureaieval/internal/project"

	"github.com/stretchr/testify/require"
)

func writeTestFile(t *testing.T, root, rel, content string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

// toneEvaluatorSource is the shape the grader requires: one top-level
// grade(sample, item) returning a float.
const toneEvaluatorSource = "def grade(sample, item) -> float:\n" +
	"    return float(len((item or {}).get(\"response\", \"\")))\n"

// The code-only settings describe a python grader. Accepting them beside a
// rubric and dropping them would leave the author believing the evaluator was
// published carrying an image and schemas it never had.
func TestValidateCodeEvaluatorFlags_RejectsCodeFlagsOnARubric(t *testing.T) {
	for flag, flags := range map[string]codeEvaluatorFlags{
		"image-tag":   {imageTag: "python:3.11"},
		"init-params": {initParams: "init.json"},
		"data-schema": {dataSchema: "schema.json"},
		"metrics":     {metrics: "metrics.json"},
	} {
		err := validateCodeEvaluatorFlags("rubric.json", flags)
		require.Error(t, err, "for --%s", flag)
		require.Contains(t, err.Error(), "--"+flag)
		require.Contains(t, err.Error(), "--from-file")

		require.NoError(t, validateCodeEvaluatorFlags("evaluator.py", flags),
			"--%s is valid with a .py --from-file", flag)
	}

	require.NoError(t, validateCodeEvaluatorFlags("rubric.json", codeEvaluatorFlags{}))
}

// One flag carries both kinds of evaluator, so the command has to say which
// one it is missing rather than naming a flag that does not exist.
func TestEvaluatorCreateRequiresASource(t *testing.T) {
	for _, verb := range []string{"create", "update"} {
		cmd := newEvaluatorWriteCommand(verb, "")
		cmd.SetArgs([]string{"tone"})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SilenceUsage = true

		err := cmd.Execute()
		require.Error(t, err, verb)
		require.Contains(t, err.Error(), "--from-file")
	}
}

// The same check the command runs must be reachable from the command, so a
// future refactor cannot leave the flags declared but unvalidated.
func TestEvaluatorCreateRejectsCodeFlagsOnARubric(t *testing.T) {
	dir := t.TempDir()
	rubric := writeTestFile(t, dir, "rubric.json", `{"dimensions":[]}`)

	cmd := newEvaluatorWriteCommand("create", "")
	cmd.SetArgs([]string{"tone", "--from-file", rubric, "--image-tag", "python:3.11"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SilenceUsage = true

	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "--image-tag")
}

// A script with no top-level grade() must be refused before a version is
// published, and the refusal must come from the command rather than from a run
// that fails minutes later.
func TestEvaluatorCreateRejectsAScriptWithoutGrade(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "tone.py",
		"class ToneEvaluator:\n    def __call__(self, **kwargs):\n        return {\"result\": 1}\n")

	cmd := newEvaluatorWriteCommand("create", "")
	cmd.SetArgs([]string{"tone", "--from-file", path})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SilenceUsage = true

	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "grade(sample, item)")
}

// The flags are the only place a code evaluator's schemas can come from: the
// grader is handed one file of source, so nothing that is not Python can
// travel with it.
func TestCodeEvaluatorOptions_ReadsTheFlags(t *testing.T) {
	empty, err := codeEvaluatorOptions(codeEvaluatorFlags{})
	require.NoError(t, err)
	require.Empty(t, empty.ImageTag)
	require.Empty(t, empty.Metrics)
	require.Empty(t, empty.DataSchema)
	require.Empty(t, empty.InitParameters)

	dir := t.TempDir()
	metricsPath := writeTestFile(t, dir, "metrics.json",
		`{"result":{"type":"continuous"}}`)
	initPath := writeTestFile(t, dir, "init.json",
		`{"type":"object","properties":{"deployment_name":{"type":"string"}}}`)
	schemaPath := writeTestFile(t, dir, "schema.json",
		`{"type":"object","properties":{"response":{"type":"string"}}}`)

	opts, err := codeEvaluatorOptions(codeEvaluatorFlags{
		imageTag:   "mcr.microsoft.com/azureml/evaluator:latest",
		metrics:    metricsPath,
		initParams: initPath,
		dataSchema: schemaPath,
	})
	require.NoError(t, err)
	require.Equal(t, "mcr.microsoft.com/azureml/evaluator:latest", opts.ImageTag)
	require.Contains(t, string(opts.Metrics), "continuous")
	require.Contains(t, string(opts.InitParameters), "deployment_name")
	require.Contains(t, string(opts.DataSchema), "response")
}

// A typo in a schema file must be reported against the flag that named it,
// not discovered by the service after a version has been published.
func TestCodeEvaluatorOptions_RejectsMalformedInput(t *testing.T) {
	dir := t.TempDir()

	bad := writeTestFile(t, dir, "metrics.json", "[1,2,3]")
	_, err := codeEvaluatorOptions(codeEvaluatorFlags{metrics: bad})
	require.Error(t, err)
	require.Contains(t, err.Error(), "--metrics")
	require.Contains(t, err.Error(), "JSON object")

	_, err = codeEvaluatorOptions(codeEvaluatorFlags{
		dataSchema: filepath.Join(dir, "absent.json"),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "--data-schema")
}

// The service rejects a code definition carrying no metrics, so a script
// published without any still has to publish with one.
func TestDefaultCodeMetricsIsAJSONObject(t *testing.T) {
	var metrics map[string]map[string]any
	require.NoError(t, json.Unmarshal(eval_api.DefaultCodeMetrics, &metrics))
	require.Len(t, metrics, 1)
	require.Contains(t, metrics, "result")
	require.Equal(t, "continuous", metrics["result"]["type"])
}

// Both kinds of evaluator source are one file, and the reconciler tells them
// apart by extension rather than by stat-ing the path.
func TestEvaluatorSourceClassificationAndFingerprint(t *testing.T) {
	root := t.TempDir()

	rubric := writeTestFile(t, root, "rubric.json", `{"dimensions":[]}`)
	script := writeTestFile(t, root, "tone.py", toneEvaluatorSource)

	require.False(t, evalcore.IsCodeEvaluatorSource(rubric))
	require.True(t, evalcore.IsCodeEvaluatorSource(script))

	rubricDigest, err := project.Fingerprint(rubric)
	require.NoError(t, err)
	scriptDigest, err := project.Fingerprint(script)
	require.NoError(t, err)
	require.NotEqual(t, rubricDigest, scriptDigest)

	// Editing the script must be noticed, or a deploy would reuse a version
	// holding the old source.
	writeTestFile(t, root, "tone.py", toneEvaluatorSource+"\n# tweak\n")
	changed, err := project.Fingerprint(script)
	require.NoError(t, err)
	require.NotEqual(t, scriptDigest, changed)

	_, err = project.Fingerprint(filepath.Join(root, "absent.py"))
	require.Error(t, err)
}
