// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package eval_api

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Seeding generation from an agent fails server-side for every agent, while the
// same request carrying only the prompt succeeds. Verified against the service:
// [prompt, agent] fails, [prompt] succeeds.
func TestWithoutAgentSourceLeavesThePrompt(t *testing.T) {
	sources := BuildGenerationSources("hosted", "support-agent", "", "answer billing questions", nil)
	assert.Equal(t, []string{"prompt", "agent"}, kindsOf(sources))

	promptOnly := WithoutAgentSource(sources)
	assert.Equal(t, []string{"prompt"}, kindsOf(promptOnly))
	assert.True(t, HasPromptSource(promptOnly),
		"the retry needs something left to generate from")
}

// Without an instruction there is no prompt, so dropping the agent leaves
// nothing and the caller must keep the original failure.
func TestWithoutAgentSourceCanLeaveNothing(t *testing.T) {
	sources := BuildGenerationSources("hosted", "support-agent", "", "", nil)
	assert.Equal(t, []string{"agent"}, kindsOf(sources))
	assert.False(t, HasPromptSource(WithoutAgentSource(sources)))
}

// A traces source is not the agent, so it survives the retry.
func TestWithoutAgentSourceKeepsTraces(t *testing.T) {
	sources := BuildGenerationSources("hosted", "support-agent", "", "seed", &TraceOptions{Days: 7})
	assert.Equal(t, []string{"prompt", "traces"}, kindsOf(WithoutAgentSource(sources)))
}

func kindsOf(sources []GenerationSource) []string {
	kinds := make([]string, 0, len(sources))
	for _, s := range sources {
		kinds = append(kinds, s.Type)
	}
	return kinds
}
