// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package cmd

import (
	"testing"

	"github.com/azure/azure-dev/cli/azd/pkg/azdext"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

func serviceWithRef(t *testing.T, ref string) *azdext.ServiceConfig {
	t.Helper()
	props, err := structpb.NewStruct(map[string]any{"$ref": ref})
	require.NoError(t, err)
	return &azdext.ServiceConfig{AdditionalProperties: props}
}

// The `$ref` an entry carries is what decides whether it still points here.
//
// Matching on name and host alone treated a service pointing at the old file as
// correctly wired, so `init --path ./quality` wrote the configuration to one
// place and left azure.yaml deploying another. That was inert while nothing read
// the entry; it stopped being inert when the directory cascade started reading
// it, because the stale value then answers on any machine that did not run this
// init -- the recorded path lives in the azd environment and does not travel
// with the repository.
func TestServiceRefIsWhatDecidesWiring(t *testing.T) {
	assert.Equal(t, "./evals/azure.eval.yaml",
		serviceRef(serviceWithRef(t, "./evals/azure.eval.yaml")))

	assert.Empty(t, serviceRef(&azdext.ServiceConfig{}),
		"an entry with no properties carries no ref")

	props, err := structpb.NewStruct(map[string]any{"host": "azure.ai.eval"})
	require.NoError(t, err)
	assert.Empty(t, serviceRef(&azdext.ServiceConfig{AdditionalProperties: props}),
		"an entry with properties but no ref carries no ref")
}

// The three outcomes are distinct, because the caller is told which happened and
// a repoint changes what `azd up` deploys.
func TestWiringOutcomesAreDistinct(t *testing.T) {
	outcomes := map[string]bool{wiringAdded: true, wiringPresent: true, wiringUpdated: true}
	assert.Len(t, outcomes, 3, "each outcome needs its own line in the summary")
}
