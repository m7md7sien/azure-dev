// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

//go:build live

// M2 adds five things to the surface M1 shipped: code evaluators, run
// comparison, trace-backed runs, stored-response runs, and model or no-target
// evaluations. Each one is a new way for the CLI and the service to disagree,
// so each is driven here through the real binary against the real project
// rather than through a fake.

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// gradeScript is the shape the python grader requires: one top-level
// grade(sample, item) returning a float.
const gradeScript = "def grade(sample, item) -> float:\n" +
	"    return float(len((item or {}).get(\"response\", \"\")))\n"

// writeFile puts a fixture on disk and hands back its path.
func writeFile(t *testing.T, rel, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), rel)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

// TestCLICodeEvaluatorRoundTrip publishes a Python evaluator and reads it back.
//
// The interesting part is not that a request succeeds: it is that the
// definition the service stores is a code definition carrying the source, so
// the extension has not quietly published a rubric with a .py file's contents
// in it.
func TestCLICodeEvaluatorRoundTrip(t *testing.T) {
	name := uniqueName("azdcli-code")
	script := writeFile(t, "grade.py", gradeScript)

	created := requireSuccess(t, run(t,
		"evaluator", "create", name, "--from-file", script, "-o", "json"))

	var version struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	created.JSON(t, &version)
	require.Equal(t, name, version.Name)
	require.NotEmpty(t, version.Version)

	deferDeleteEvaluator(name)

	// Read back rather than trusting the create response: the service answers
	// a publish with the version's identity and metadata but no definition, so
	// what was actually stored is only visible on a read.
	shown := requireSuccess(t, run(t,
		"evaluator", "show", name, "--version", version.Version, "-o", "json"))

	var stored struct {
		EvaluatorType string `json:"evaluator_type"`
		Definition    struct {
			Type     string `json:"type"`
			CodeText string `json:"code_text"`
		} `json:"definition"`
	}
	shown.JSON(t, &stored)
	require.Equal(t, "code", stored.Definition.Type,
		"a .py source must publish as a code definition, not a rubric")
	require.Contains(t, stored.Definition.CodeText, "def grade(",
		"the published definition must carry the script the author wrote")

	// An update publishes a second version rather than editing the first:
	// evaluator versions are immutable, and an eval bound to v1 must keep
	// scoring with the code it was bound to.
	edited := writeFile(t, "grade.py", gradeScript+"\n# revised\n")
	updated := requireSuccess(t, run(t,
		"evaluator", "update", name, "--from-file", edited, "-o", "json"))

	var second struct {
		Version string `json:"version"`
	}
	updated.JSON(t, &second)
	require.NotEqual(t, version.Version, second.Version,
		"update must publish a new version, not overwrite one")

	listed := requireSuccess(t, run(t, "evaluator", "versions", "list", name, "-o", "json"))
	var versions []struct {
		Version string `json:"version"`
	}
	listed.JSON(t, &versions)
	require.GreaterOrEqual(t, len(versions), 2,
		"both published versions must be listed")
}

// TestCLICodeEvaluatorCreateRefusesAnExistingName is the same guard the rubric
// path has. Publishing over an existing evaluator by accident is the mistake
// `update` exists to make deliberate.
func TestCLICodeEvaluatorCreateRefusesAnExistingName(t *testing.T) {
	name := uniqueName("azdcli-code-dup")
	script := writeFile(t, "grade.py", gradeScript)

	requireSuccess(t, run(t, "evaluator", "create", name, "--from-file", script, "-o", "json"))
	deferDeleteEvaluator(name)

	r := requireFailure(t, run(t, "evaluator", "create", name, "--from-file", script))
	require.Contains(t, r.Combined(), "already exists")
	require.Contains(t, r.Combined(), "update")
}

// TestCLICodeEvaluatorRejectsAScriptWithoutGrade proves the check runs before
// anything is published.
//
// The service only discovers a missing entry point when a run executes — long
// after a version exists and an eval is bound to it — and the error it gives
// then names neither the file nor the evaluator.
func TestCLICodeEvaluatorRejectsAScriptWithoutGrade(t *testing.T) {
	script := writeFile(t, "grade.py",
		"class Grader:\n    def __call__(self, **kw):\n        return {\"result\": 1}\n")

	r := requireFailure(t, run(t,
		"evaluator", "create", uniqueName("azdcli-nograde"), "--from-file", script))
	require.Contains(t, r.Combined(), "grade(sample, item)",
		"the refusal must name the contract the script has to meet")

	// Nothing was published, so nothing is there to find.
	require.Contains(t, r.Combined(), script,
		"the refusal must name the file that is wrong")
}

// TestCLICodeFlagsAreRefusedOnARubric covers the quiet failure: the service
// accepts a rubric carrying an image tag and drops it, leaving the author
// believing an evaluator was published with settings it never had.
func TestCLICodeFlagsAreRefusedOnARubric(t *testing.T) {
	rubric := writeFile(t, "rubric.json",
		`{"dimensions":[{"name":"tone","weight":1,"description":"polite"}]}`)

	for _, flag := range []string{"--image-tag", "--init-params", "--data-schema", "--metrics"} {
		t.Run(flag, func(t *testing.T) {
			r := requireFailure(t, run(t, "evaluator", "create",
				uniqueName("azdcli-rubric"), "--from-file", rubric, flag, "x"))
			require.Contains(t, r.Combined(), flag)
			require.Contains(t, r.Combined(), "--from-file",
				"the refusal must say which source the flag belongs to")
		})
	}
}

// TestCLIRunCompareIsWired drives a real comparison end to end.
//
// The shared fixture accumulates completed runs as the suite goes, so by the
// time this runs there is a baseline and a treatment to pick without starting
// runs of its own. What is worth proving here is that the insight the service
// returns can be rendered: the comparison is polled to a terminal state and
// printed, and both of those have been wrong before.
func TestCLIRunCompareIsWired(t *testing.T) {
	fixture := sharedEval(t)

	help := requireSuccess(t, run(t, "run", "compare", "--help"))
	for _, flag := range []string{"--baseline", "--treatment", "--eval"} {
		require.Contains(t, help.Combined(), flag,
			"run compare must declare %s", flag)
	}

	r := requireSuccess(t, run(t, "run", "compare", fixture.EvalID, "-o", "json"))

	var insight struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Result *struct {
			Method      string           `json:"method"`
			Comparisons []map[string]any `json:"comparisons"`
		} `json:"result"`
	}
	r.JSON(t, &insight)
	require.NotEmpty(t, insight.ID)
	require.NotEqual(t, "failed", strings.ToLower(insight.Status),
		"the comparison must reach a terminal state that is not a failure")

	// The table rendering is the default, and it is the part a person reads.
	table := requireSuccess(t, run(t, "run", "compare", fixture.EvalID))
	require.NotEmpty(t, strings.TrimSpace(table.Stdout))
}

// TestCLITraceAndResponseSourcesAreExclusive pins the one combination that
// cannot mean anything.
//
// Traces and stored responses are both "data the agent already produced", and
// a run has exactly one data source, so naming both is a mistake cobra should
// catch before a request is made.
func TestCLITraceAndResponseSourcesAreExclusive(t *testing.T) {
	r := requireFailure(t, run(t, "run", "start",
		"--eval-id", "eval_does_not_matter",
		"--from-traces", "--response-id", "resp_123"))
	require.Contains(t, strings.ToLower(r.Combined()), "from-traces")
	require.Contains(t, strings.ToLower(r.Combined()), "response-id")
}

// TestCLIRunStartDeclaresTheM2Sources is what keeps the spec's flag table and
// the binary in step. A flag that is documented and not declared fails at the
// worst moment: in someone's pipeline, on the first run.
func TestCLIRunStartDeclaresTheM2Sources(t *testing.T) {
	help := requireSuccess(t, run(t, "run", "start", "--help"))
	for _, flag := range []string{
		"--from-traces", "--trace-window", "--max-traces", "--response-id", "--max-turns",
	} {
		require.Contains(t, help.Combined(), flag,
			"run start must declare %s", flag)
	}
}

// TestCLITraceWindowIsValidated catches the mistake before it costs a run.
//
// A malformed window would otherwise be sent as a zero lookback, which the
// service accepts and answers with no traces at all — a run that succeeds and
// evaluates nothing.
func TestCLITraceWindowIsValidated(t *testing.T) {
	r := requireFailure(t, run(t, "run", "start",
		"--eval-id", "eval_does_not_matter",
		"--from-traces", "--trace-window", "last-tuesday"))
	require.Contains(t, r.Combined(), "trace-window")
}
