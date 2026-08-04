// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package cmd

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"azureaieval/internal/pkg/eval_api"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An unset --max-traces is not "unbounded": a window selects traces by time,
// so nothing else bounds how many conversations a busy agent produces. The
// bound is a default rather than a ceiling, so a caller can ask for more.
func TestResolveMaxTraces(t *testing.T) {
	got, err := resolveMaxTraces(0)
	require.NoError(t, err)
	assert.Equal(t, defaultMaxTraces, got)

	got, err = resolveMaxTraces(25)
	require.NoError(t, err)
	assert.Equal(t, 25, got)

	got, err = resolveMaxTraces(1000)
	require.NoError(t, err)
	assert.Equal(t, 1000, got, "a larger sweep is the caller's to ask for")

	_, err = resolveMaxTraces(-1)
	require.ErrorContains(t, err, "negative")
}

// A window is only sent when one was asked for; the service defaults it
// otherwise.
func TestNewTracesDataSource_OmitsAnUnsetWindow(t *testing.T) {
	ds := eval_api.NewTracesDataSource("support-agent", 0, time.Time{}, 0)
	assert.Equal(t, eval_api.EvalRunDataSourceTypeTraces, ds.Type)
	assert.Equal(t, "support-agent", ds.AgentName)

	raw, err := json.Marshal(ds)
	require.NoError(t, err)
	body := string(raw)
	assert.NotContains(t, body, "lookback_hours")
	assert.NotContains(t, body, "end_time")
	assert.NotContains(t, body, "max_traces")
	assert.NotContains(t, body, "input_messages", "traces carry no template")
}

// The service reads `lookback_hours` and has no start bound. Sending a
// start_time is accepted and dropped, which silently leaves the default seven
// days in place, so the window has to travel as hours.
func TestNewTracesDataSource_SendsAWindowTheServiceReads(t *testing.T) {
	ds := eval_api.NewTracesDataSource("support-agent", 30*24, time.Time{}, 25)
	assert.Equal(t, 720, ds.LookbackHours)
	assert.Equal(t, 25, ds.MaxTraces)

	raw, err := json.Marshal(ds)
	require.NoError(t, err)
	body := string(raw)
	assert.Contains(t, body, `"lookback_hours":720`)
	assert.NotContains(t, body, "start_time",
		"the service drops start_time and falls back to its default window")
}

// The reason a run failed is the only actionable part of the response, so it
// has to survive into the output.
func TestRunFailureMessage(t *testing.T) {
	var run eval_api.OpenAIEvalRun
	require.NoError(t, json.Unmarshal([]byte(`{
      "id": "evalrun_x", "status": "failed",
      "error": { "code": "UserError", "message": "  No trace data found for agent_name 'a'.  " }
    }`), &run))
	assert.Equal(t, "No trace data found for agent_name 'a'.", run.Failure())

	// The field is present and null-valued on success, so presence alone
	// must not read as failure.
	var ok eval_api.OpenAIEvalRun
	require.NoError(t, json.Unmarshal([]byte(`{
      "id": "evalrun_y", "status": "completed",
      "error": { "code": null, "message": null }
    }`), &ok))
	assert.Empty(t, ok.Failure())

	var absent eval_api.OpenAIEvalRun
	require.NoError(t, json.Unmarshal([]byte(`{"id":"evalrun_z","status":"completed"}`), &absent))
	assert.Empty(t, absent.Failure())

	var nilRun *eval_api.OpenAIEvalRun
	assert.Empty(t, nilRun.Failure())
}

// The ids travel as ordinary JSONL rows with a mapping pointing at the field
// that holds each one; that is how the service finds the chat history.
func TestNewResponsesDataSource(t *testing.T) {
	ds := eval_api.NewResponsesDataSource([]string{"resp_a", "resp_b"}, 10)
	assert.Equal(t, eval_api.EvalRunDataSourceTypeResponses, ds.Type)
	require.NotNil(t, ds.ItemGenerationParams)
	assert.Equal(t, "response_retrieval", ds.ItemGenerationParams.Type)
	assert.Equal(t, 10, ds.ItemGenerationParams.MaxNumTurns)
	assert.Equal(t,
		map[string]string{"response_id": "{{item.response_id}}"},
		ds.ItemGenerationParams.DataMapping)

	raw, err := json.Marshal(ds)
	require.NoError(t, err)
	body := string(raw)
	assert.Contains(t, body, `"response_id":"resp_a"`)
	assert.Contains(t, body, `"response_id":"resp_b"`)
	assert.NotContains(t, body, "agent_name", "responses carry no agent")
}

// An unset turn limit is left to the service rather than sent as zero.
func TestNewResponsesDataSource_OmitsAnUnsetTurnLimit(t *testing.T) {
	ds := eval_api.NewResponsesDataSource([]string{"resp_a"}, 0)
	raw, err := json.Marshal(ds)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "max_num_turns")
}

func TestRenderRun_ShowsTheFailureReason(t *testing.T) {
	var buf bytes.Buffer
	run := &eval_api.OpenAIEvalRun{
		ID:     "evalrun_x",
		Status: "failed",
		Error:  &eval_api.JobError{Code: "UserError", Message: "No trace data found."},
	}
	require.NoError(t, renderRun(&buf, run, nil))
	assert.Contains(t, buf.String(), "failed")
	assert.Contains(t, buf.String(), "No trace data found.")

	var clean bytes.Buffer
	require.NoError(t, renderRun(&clean, &eval_api.OpenAIEvalRun{ID: "evalrun_y", Status: "completed"}, nil))
	assert.NotContains(t, clean.String(), "  \n", "a successful run gains no blank reason line")
}

// A window the CLI cannot read has to be refused, not defaulted.
//
// Zero means "no lookback": the service accepts it, matches no trace, and the
// run completes having evaluated nothing. A typo would cost a run and report
// success, which is worse than the typo.
func TestParseWindowHours(t *testing.T) {
	for _, tc := range []struct {
		window string
		hours  int
	}{
		{"", 0},
		{"7d", 7 * 24},
		{"7", 7 * 24},
		{" 30D ", 30 * 24},
	} {
		hours, err := parseWindowHours(tc.window)
		require.NoError(t, err, "window %q", tc.window)
		assert.Equal(t, tc.hours, hours, "window %q", tc.window)
	}

	for _, bad := range []string{"last-tuesday", "7 days", "d", "-3d", "0", "0d"} {
		_, err := parseWindowHours(bad)
		require.Error(t, err, "window %q must be refused", bad)
		assert.Contains(t, err.Error(), "trace-window",
			"the refusal must name the flag that is wrong")
	}
}
