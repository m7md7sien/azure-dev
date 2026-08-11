// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package cmd

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The service reports agent-seeded generation failures two ways, and the retry
// has to recognise both or it never fires.
func TestIsAgentSeededGenerationFailure(t *testing.T) {
	wrapped := fmt.Errorf("dataset generation job %s: %w", "datagen-1",
		errors.New(`dataset generation failed with status "failed": `+
			`Something went wrong during data generation. Please try again.`))
	assert.True(t, isAgentSeededGenerationFailure(wrapped),
		"the message arrives wrapped in the job id, so matching must survive that")

	assert.True(t, isAgentSeededGenerationFailure(errors.New("DataGenerationJobSystemError")))

	assert.False(t, isAgentSeededGenerationFailure(nil))
	assert.False(t, isAgentSeededGenerationFailure(errors.New("quota exceeded")),
		"an unrelated failure must not be retried without the agent")
}
