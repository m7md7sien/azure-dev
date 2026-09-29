// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package cmd

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"azureaieval/internal/pkg/eval_api"
	"azureaieval/internal/pkg/evalcore"
	"azureaieval/internal/project"

	"github.com/azure/azure-dev/cli/azd/pkg/azdext"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMappingAgreedDefaultsOnProductionWire(t *testing.T) {
	for _, source := range []string{"dataset", "traces", "traces with target filter"} {
		for _, level := range []string{"turn", "conversation"} {
			for _, catalog := range []string{"missing", "empty", "broad"} {
				t.Run(source+"/"+level+"/"+catalog, func(t *testing.T) {
					want := map[string]string{
						"query": "{{item.query}}", "response": "{{item.response}}",
						"tool_calls": "{{item.tool_calls}}", "tool_definitions": "{{item.tool_definitions}}",
					}
					if level == "conversation" {
						want = map[string]string{
							"messages": "{{item.messages}}", "tool_definitions": "{{item.tool_definitions}}",
						}
					}
					var posted eval_api.CreateOpenAIEvalRequest
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						assert.Equal(t, http.MethodPost, r.Method)
						assert.Equal(t, "/openai/v1/evals", r.URL.Path)
						assert.NoError(t, json.NewDecoder(r.Body).Decode(&posted))
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(`{"id":"eval_defaults"}`))
					}))
					defer srv.Close()
					ec := evalContextFor(srv)
					group := &project.Eval{Name: "defaults", Dataset: "d", EvaluationLevel: level}
					schemas := map[string]*eval_api.EvaluatorSummary{}
					for _, name := range []string{"builtin.task_completion", "builtin.coherence", "builtin.groundedness"} {
						group.Evaluators = append(group.Evaluators, evalcore.EvaluatorRef{Evaluator: name})
						switch catalog {
						case "empty":
							schemas[name] = schema(name, nil, nil, nil, nil, "turn", "conversation")
						case "broad":
							schemas[name] = schema(name, nil, []string{
								"query", "response", "messages", "tool_calls", "tool_definitions", "context", "ground_truth",
							}, nil, nil, "turn", "conversation")
						}
					}
					columns := map[string]bool{
						"query": true, "response": true, "messages": true, "context": true, "ground_truth": true,
					}
					if strings.HasPrefix(source, "traces") {
						group.Dataset = ""
						group.Source = &project.SourceDecl{Type: project.SourceTypeTraces, AgentName: "recorded-agent"}
						columns = nil
						if source == "traces with target filter" {
							group.Target = &project.Target{Type: "agent", Name: "recorded-agent"}
						}
					}
					request, err := buildEvalRequest(group, schemas, columns)
					require.NoError(t, err)
					_, err = ec.evalClient.CreateOpenAIEval(t.Context(), request)
					require.NoError(t, err)
					require.Len(t, posted.TestingCriteria, 3)
					for _, criterion := range posted.TestingCriteria {
						require.Equal(t, want, criterion.DataMapping, criterion.EvaluatorName)
					}
				})
			}
		}
	}
}

func TestMappingRequiredAdditionalInputIsNeverInferred(t *testing.T) {
	for _, columns := range []map[string]bool{nil, {}, {"query": true, "response": true, "context": true}} {
		group := &project.Eval{
			Name: "grounding", Dataset: "d",
			Evaluators: []evalcore.EvaluatorRef{{Evaluator: "builtin.groundedness"}},
		}
		contract := schema("builtin.groundedness", []string{"response", "context"},
			[]string{"query", "response", "context"}, nil, nil, "turn")
		_, err := buildEvalRequest(group, map[string]*eval_api.EvaluatorSummary{contract.Name: contract}, columns)
		require.ErrorContains(t, err, "context")
		local, ok := errors.AsType[*azdext.LocalError](err)
		require.True(t, ok)
		require.Contains(t, local.Suggestion, "data_mapping")
		require.Contains(t, local.Suggestion, "actual source columns")
	}
}

func TestMappingRequiredAdditionalInputUsesExplicitSource(t *testing.T) {
	group := &project.Eval{
		Name: "grounding", Dataset: "d",
		Evaluators: []evalcore.EvaluatorRef{{
			Evaluator: "builtin.groundedness",
			DataMapping: map[string]string{
				"query": "{{item.question}}", "response": "{{item.answer}}",
				"context": "{{item.facts}}", "ground_truth": "{{item.expected}}",
			},
		}},
	}
	contract := schema("builtin.groundedness", []string{"response", "context"}, []string{"response", "context"},
		nil, nil, "turn")
	columns := map[string]bool{"question": true, "answer": true, "facts": true, "expected": true}
	request, err := buildEvalRequest(group, map[string]*eval_api.EvaluatorSummary{contract.Name: contract}, columns)
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"query": "{{item.question}}", "response": "{{item.answer}}", "context": "{{item.facts}}",
		"ground_truth": "{{item.expected}}", "tool_calls": "{{item.tool_calls}}",
		"tool_definitions": "{{item.tool_definitions}}",
	}, request.TestingCriteria[0].DataMapping)
	delete(columns, "facts")
	_, err = buildEvalRequest(group, map[string]*eval_api.EvaluatorSummary{contract.Name: contract}, columns)
	require.ErrorContains(t, err, "facts")
}
