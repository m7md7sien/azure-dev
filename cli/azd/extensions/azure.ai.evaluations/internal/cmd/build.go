// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package cmd

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"azureaieval/internal/exterrors"
	"azureaieval/internal/messages"
	"azureaieval/internal/pkg/eval_api"
	"azureaieval/internal/pkg/evalcore"
	"azureaieval/internal/project"
)

// evaluatorSchemas indexes the published contract of every evaluator a group
// can reference.
//
// Built-ins have to be asked for separately. An unfiltered list returns only
// the project's own evaluators, so relying on it leaves every built-in without
// a schema and falling back to legacyInputs — which happens to match
// query/response and so looks right for the common evaluators while quietly
// dropping the fields anything else needs.
//
// A failure is deliberately not fatal: without schemas the builder falls back
// to the agent-target shape, which is what it always used to send.
func (ec *evalContext) evaluatorSchemas(ctx context.Context) map[string]*eval_api.EvaluatorSummary {
	if ec.schemas != nil {
		return ec.schemas
	}

	index := map[string]*eval_api.EvaluatorSummary{}
	complete := true
	for _, filter := range []string{"", eval_api.EvaluatorTypeBuiltin} {
		list, err := ec.evalClient.ListEvaluators(ctx, filter, ProjectEndpointAPIVersion)
		if err != nil {
			complete = false
			continue
		}
		maps.Copy(index, list.ByName())
	}
	if len(index) == 0 {
		return nil
	}
	// Only a complete read is worth keeping. Caching a half of it would leave
	// every later eval validating against legacyInputs, which accepts fields
	// the evaluator never declared.
	if complete {
		ec.schemas = index
	}
	return index
}

// sampleBindings are the fields an agent target produces at run time. Anything
// an evaluator accepts that is not in this set has to come from a dataset
// column instead.
var sampleBindings = map[string]string{
	"response":         "{{sample.output_items}}",
	"tool_calls":       "{{sample.tool_calls}}",
	"tool_definitions": "{{sample.tool_definitions}}",
}

// sampleBindingsFor returns the run-time bindings a target of this kind can
// satisfy. An empty target kind means nothing is invoked, so nothing is bound.
//
// A model target supplies plain text, not an agent's structured tool output.
func sampleBindingsFor(targetType string) map[string]string {
	if targetType == project.TargetTypeAgent {
		return sampleBindings
	}
	if targetType == project.TargetTypeModel {
		return map[string]string{"response": "{{sample.output_text}}"}
	}
	return nil
}

// legacyInputs is the mapping used when the service publishes no schema for an
// evaluator, which is the case for freshly uploaded custom evaluators. It
// includes both interaction formats; selectLevelFields chooses one.
var legacyInputs = []string{"query", "response", "tool_calls", "tool_definitions", "messages"}

// criterionPlan is the resolved binding for one evaluator.
type criterionPlan struct {
	dataMapping map[string]string
	initParams  map[string]any
	// itemFields are the fields sourced from dataset columns; they have to be
	// declared in the item schema.
	itemFields []string
}

// conversationField carries a whole conversation. The service rejects a
// mapping that pairs it with the turn-level fields:
//
//	Evaluator 'builtin.task_completion' has both 'messages' and
//	'query'/'response' in data_mapping. Use 'messages' for conversation-level
//	evaluation or 'query'/'response' for turn-level evaluation, but not both.
const conversationField = "messages"

// turnFields are the per-turn counterparts to conversationField.
var turnFields = []string{"query", "response"}

// selectLevelFields resolves the conversation/turn exclusivity for evaluators
// that accept both shapes, keeping whichever matches the evaluation level.
// The interaction formats are alternatives, not cumulative requirements.
func selectLevelFields(accepted []string, level string) []string {
	acceptsConversation := false
	acceptsTurn := false
	for _, field := range accepted {
		if field == conversationField {
			acceptsConversation = true
		}
		for _, turn := range turnFields {
			if field == turn {
				acceptsTurn = true
			}
		}
	}
	if !acceptsConversation || !acceptsTurn {
		return accepted
	}

	drop := map[string]bool{}
	if strings.EqualFold(level, project.EvaluationLevelConversation) {
		for _, turn := range turnFields {
			drop[turn] = true
		}
	} else {
		drop[conversationField] = true
	}

	kept := make([]string, 0, len(accepted))
	for _, field := range accepted {
		if drop[field] {
			continue
		}
		kept = append(kept, field)
	}
	return kept
}

// planCriterion shapes one evaluator's bindings from its published contract.
//
// Evaluators do not share an input contract: builtin.similarity needs
// ground_truth, builtin.retrieval needs context, and builtin.ifeval needs
// instruction_id_list. Sending one fixed mapping to all of them earns a
// service-side MissingRequiredDataMapping rejection, so the mapping is derived
// per evaluator and anything unsatisfiable is reported before the request is
// sent.
func planCriterion(
	ref evalcore.EvaluatorRef,
	schema *eval_api.EvaluatorSummary,
	targetBindings map[string]string,
	datasetColumns map[string]bool,
	level string,
) (*criterionPlan, error) {
	accepted := legacyInputs
	var required []string
	// A published schema is authoritative even when it is empty: an empty
	// property set means the evaluator accepts nothing, which is different from
	// publishing no schema at all.
	if dataSchema := schema.DataSchema(); dataSchema != nil {
		accepted = dataSchema.PropertyNames()
		required = dataSchema.Required
	}
	_, explicitMessages := ref.DataMapping[conversationField]
	_, explicitQuery := ref.DataMapping["query"]
	_, explicitResponse := ref.DataMapping["response"]
	if explicitMessages && (explicitQuery || explicitResponse) {
		return nil, exterrors.Validation(exterrors.CodeConflictingArguments,
			fmt.Sprintf("evaluator %q: data_mapping combines messages with query or response", ref.Evaluator),
			"Map messages for a complete interaction, or query and response separately, but not both.")
	}
	mappingLevel := level
	switch {
	case explicitMessages:
		mappingLevel = project.EvaluationLevelConversation
	case explicitQuery || explicitResponse:
		mappingLevel = project.EvaluationLevelTurn
	case len(targetBindings) == 0 && datasetColumns[conversationField] &&
		!datasetColumns["query"] && !datasetColumns["response"]:
		mappingLevel = project.EvaluationLevelConversation
	}
	if schema.DataSchema() == nil && strings.EqualFold(mappingLevel, project.EvaluationLevelConversation) {
		accepted = []string{conversationField}
	}
	allAccepted := accepted
	accepted = selectLevelFields(accepted, mappingLevel)

	plan := &criterionPlan{
		dataMapping: map[string]string{},
		initParams:  map[string]any{},
	}

	for _, field := range accepted {
		if (explicitMessages && contains(turnFields, field)) ||
			((explicitQuery || explicitResponse) && field == conversationField) {
			continue
		}
		if binding, ok := targetBindings[field]; ok {
			plan.dataMapping[field] = binding
			continue
		}
		// Everything else comes from the dataset. When the columns are known,
		// bind only the ones that exist so optional fields stay unbound rather
		// than resolving to nothing at run time.
		if datasetColumns != nil && !datasetColumns[field] {
			continue
		}
		plan.dataMapping[field] = fmt.Sprintf("{{item.%s}}", field)
	}

	// A declared mapping is the author saying the inference got it wrong, so it
	// wins. Anything it binds to an item column is a column the schema has to
	// declare, whether or not inference found it.
	for _, field := range slices.Sorted(maps.Keys(ref.DataMapping)) {
		binding := ref.DataMapping[field]
		if strings.TrimSpace(binding) == "" {
			return nil, exterrors.Validation(exterrors.CodeInvalidParameter,
				fmt.Sprintf("evaluator %q: data_mapping for %q is empty", ref.Evaluator, field),
				"Supply a dataset or sample binding, or remove the entry to use its default.")
		}
		plan.dataMapping[field] = binding
	}
	// Derive columns from the final mapping, not from defaults an override replaced.
	for _, field := range slices.Sorted(maps.Keys(plan.dataMapping)) {
		binding := plan.dataMapping[field]
		if column, ok := itemColumn(binding); ok && !contains(plan.itemFields, column) {
			plan.itemFields = append(plan.itemFields, column)
		}
	}

	var missing []string
	for _, field := range required {
		if contains(allAccepted, field) && !contains(accepted, field) {
			if strings.EqualFold(mappingLevel, project.EvaluationLevelConversation) {
				if _, ok := plan.dataMapping[conversationField]; !ok && !contains(missing, conversationField) {
					missing = append(missing, conversationField)
				}
			} else if plan.dataMapping["query"] == "" && plan.dataMapping["response"] == "" {
				missing = append(missing, "query", "response")
			}
			continue
		}
		if _, ok := plan.dataMapping[field]; !ok {
			missing = append(missing, field)
		}
	}
	if len(missing) > 0 {
		return nil, messages.EvaluatorNeedsFields(ref.Evaluator, missing)
	}

	if !schema.SupportsLevel(level) {
		return nil, messages.EvaluatorLevelUnsupported(
			ref.Evaluator, level, schema.SupportedEvaluationLevels)
	}

	initSchema := schema.InitSchema()
	accepts := func(name string) bool {
		// Only an absent schema falls back to the historical parameters;
		// builtin.ifeval publishes an empty one and takes none.
		if initSchema == nil {
			return name == "deployment_name" || name == "threshold"
		}
		return initSchema.Accepts(name)
	}

	// Evaluators disagree on what the judge model is called: built-ins declare
	// deployment_name, custom rubrics declare model. The declaration names one
	// of them; bind whichever the evaluator actually accepts rather than
	// forwarding a spelling it will reject.
	for name, value := range ref.InitializationParameters {
		if accepts(name) {
			plan.initParams[name] = value
			continue
		}
		if alias, ok := judgeModelAliases[name]; ok && accepts(alias) {
			plan.initParams[alias] = value
		}
	}
	if level != "" && accepts("evaluation_level") {
		plan.initParams["evaluation_level"] = level
	}

	if initSchema != nil {
		var missingInit []string
		for _, name := range initSchema.Required {
			if _, ok := plan.initParams[name]; !ok {
				missingInit = append(missingInit, name)
			}
		}
		if len(missingInit) > 0 {
			return nil, messages.EvaluatorNeedsInitParams(ref.Evaluator, missingInit)
		}
	}

	return plan, nil
}

// checkEvaluatorRequirements refuses a declaration the evaluators cannot
// satisfy, before anything is published.
//
// The same checks happen while building the request, but that runs after the
// datasets and evaluators have been pushed -- so a missing judge deployment
// cost an immutable dataset version per attempt, and the version numbers climb
// whether or not the eval was ever created. Only what the published contract
// alone can settle is checked here: the data mapping needs the dataset's
// columns, which is a separate question.
func checkEvaluatorRequirements(
	eval *project.Eval,
	schemas map[string]*eval_api.EvaluatorSummary,
) error {
	level := resolveLevel(eval)
	for _, ref := range eval.Evaluators {
		schema := schemas[ref.Evaluator]
		if schema == nil {
			// Nothing published to check against. The service still gets the
			// last word, which is what happened before this existed.
			continue
		}
		if !schema.SupportsLevel(level) {
			return messages.EvaluatorLevelUnsupported(
				ref.Evaluator, level, schema.SupportedEvaluationLevels)
		}

		initSchema := schema.InitSchema()
		if initSchema == nil {
			continue
		}
		var missing []string
		for _, name := range initSchema.Required {
			if declaredInitParam(ref, name) {
				continue
			}
			if name == "evaluation_level" && level != "" {
				continue
			}
			missing = append(missing, name)
		}
		if len(missing) > 0 {
			return messages.EvaluatorNeedsInitParams(ref.Evaluator, missing)
		}
	}
	return nil
}

// declaredInitParam reports whether the reference supplies a parameter under
// either spelling of the judge deployment.
func declaredInitParam(ref evalcore.EvaluatorRef, name string) bool {
	if _, ok := ref.InitializationParameters[name]; ok {
		return true
	}
	if alias, ok := judgeModelAliases[name]; ok {
		_, declared := ref.InitializationParameters[alias]
		return declared
	}
	return false
}

// judgeModelAliases maps the two spellings of the judge deployment onto each
// other, so one declaration works whichever the evaluator publishes.
var judgeModelAliases = map[string]string{
	"deployment_name": "model",
	"model":           "deployment_name"}

// itemColumn reads the dataset column out of an `{{item.<name>}}` binding.
func itemColumn(binding string) (string, bool) {
	const prefix, suffix = "{{item.", "}}"
	if !strings.HasPrefix(binding, prefix) || !strings.HasSuffix(binding, suffix) {
		return "", false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(binding, prefix), suffix)
	if name == "" {
		return "", false
	}
	return name, true
}

func contains(values []string, want string) bool {
	return slices.Contains(values, want)
}

// buildEvalRequest converts an eval declaration into the create
// request. Each evaluator becomes a testing criterion bound to its own
// contract, and the item schema declares every dataset column those bindings
// reference.
//
// schemas may be nil or partial; an evaluator with no published contract falls
// back to the agent-target shape. datasetColumns may be nil, meaning the
// columns are unknown and every accepted field is assumed present.
func buildEvalRequest(
	group *project.Eval,
	schemas map[string]*eval_api.EvaluatorSummary,
	datasetColumns map[string]bool,
) (*eval_api.CreateOpenAIEvalRequest, error) {
	metadata := map[string]string{}
	hasTarget := group.Target != nil && group.Target.Name != ""
	targetType := ""
	if hasTarget {
		metadata[metaAgent] = group.Target.Name
		targetType = group.Target.Type
		if targetType == "" {
			targetType = project.TargetTypeAgent
		}
	}
	targetBindings := sampleBindingsFor(targetType)
	retrievedResponses := group.Source != nil && group.Source.Type == project.SourceTypeResponses
	if retrievedResponses {
		// Retrieved responses expose generated sample output too, even though
		// this run does not invoke a target.
		targetBindings = sampleBindings
	}

	// A simulation is graded on the conversations the run creates, not on the
	// seed rows it creates them from. The seeds carry test_case_description and
	// simulation_configuration; the graded item carries `messages`. Binding the seed
	// columns here is how a conversation evaluator ended up either refused at
	// deploy for a column the seeds do not have, or created with no binding for
	// the conversation it was meant to score.
	//
	// The sample namespace goes with it: the service holds the conversation
	// itself, so there is no per-row target invocation to produce `sample`.
	simulated := group.Simulation != nil
	if simulated {
		datasetColumns = map[string]bool{conversationField: true}
		targetBindings = nil
	}

	metadata[metaEvalName] = group.Name
	// The create request has no description field, so the group's own
	// description rides in metadata rather than being dropped.
	if group.Description != "" {
		metadata[metaDescription] = group.Description
	}

	level := group.EvaluationLevel

	req := &eval_api.CreateOpenAIEvalRequest{
		Name:     group.Name,
		Metadata: metadata,
	}

	itemFields := map[string]bool{}
	itemProperties := map[string]any{}

	for _, ref := range group.Evaluators {
		schema := schemas[ref.Evaluator]
		if schema == nil {
			schema = &eval_api.EvaluatorSummary{Name: ref.Evaluator}
		}

		bindings := targetBindings
		// The preview trace source has a separate, unverified turn contract.
		// Do not infer its output namespace from target/response examples.
		if group.Source == nil || group.Source.Type != project.SourceTypeTraces {
			if dataSchema := schema.DataSchema(); dataSchema != nil &&
				bindings["response"] == "{{sample.output_items}}" {
				if property, ok := dataSchema.Properties["response"].(map[string]any); ok &&
					property["type"] == "string" {
					bindings = maps.Clone(bindings)
					bindings["response"] = "{{sample.output_text}}"
				}
			}
		}
		plan, err := planCriterion(ref, schema, bindings, datasetColumns, level)
		if err != nil {
			return nil, err
		}

		criterion := eval_api.TestingCriterion{
			Type: "azure_ai_evaluator",
			// Name labels the criterion in results and defaults to the
			// evaluator without its builtin prefix; EvaluatorName keeps it.
			Name:          ref.CriterionName(),
			EvaluatorName: ref.Evaluator,
			DataMapping:   plan.dataMapping,
		}
		if ref.Version != "" {
			criterion.EvaluatorVersion = ref.Version
		}
		if len(plan.initParams) > 0 {
			criterion.InitializationParameters = plan.initParams
		}
		for _, field := range plan.itemFields {
			itemFields[field] = true
		}
		for _, field := range slices.Sorted(maps.Keys(plan.dataMapping)) {
			binding := plan.dataMapping[field]
			if column, ok := itemColumn(binding); ok {
				property := itemProperty(field)
				if prior, exists := itemProperties[column]; exists && !reflect.DeepEqual(prior, property) {
					itemProperties[column] = map[string]any{"allOf": []any{prior, property}}
				} else {
					itemProperties[column] = property
				}
			}
		}

		req.TestingCriteria = append(req.TestingCriteria, criterion)
	}

	// Declared whether or not a criterion bound it: the conversations a
	// simulation creates are what its rows hold, and a schema that omits the
	// column they arrive in describes a different dataset.
	if simulated {
		itemFields[conversationField] = true
	}

	req.DataSourceConfig = &eval_api.DataSourceConfig{
		Type:                "custom",
		IncludeSampleSchema: (hasTarget || retrievedResponses) && !simulated,
		ItemSchema:          itemSchema(itemFields),
	}
	properties := req.DataSourceConfig.ItemSchema["properties"].(map[string]any)
	maps.Copy(properties, itemProperties)
	if simulated {
		req.DataSourceConfig.ItemSchema["required"] = []string{conversationField}
	}

	return req, nil
}

// itemSchema declares the dataset columns the criteria bind to. It always
// declares at least `query`, the column an agent target reads.
func itemSchema(fields map[string]bool) map[string]any {
	if len(fields) == 0 {
		fields = map[string]bool{"query": true}
	}
	properties := map[string]any{}
	for field := range fields {
		properties[field] = itemProperty(field)
	}
	return map[string]any{
		"type":       "object",
		"properties": properties,
	}
}

// itemProperty describes the standard interaction formats, including tool
// results needed for groundedness. Aliased columns use the evaluator field's
// shape, not the spelling the dataset author chose for the column.
func itemProperty(field string) map[string]any {
	messages := map[string]any{"type": "array", "items": map[string]any{"type": "object"}}
	switch field {
	case conversationField:
		return messages
	case "query", "response":
		return map[string]any{"anyOf": []any{map[string]any{"type": "string"}, messages}}
	case "tool_definitions":
		return map[string]any{"anyOf": []any{
			map[string]any{"type": "string"}, map[string]any{"type": "object"}, messages,
		}}
	default:
		return map[string]any{"type": "string"}
	}
}
