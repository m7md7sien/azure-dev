// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package project

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `$ref` inside azure.eval.yaml is resolved on deploy and rejected everywhere else.
//
// azd core resolves `$ref` recursively over the whole service config
// (`foundry.ResolveFileRefs`), and the eval service target calls it, so `azd up`
// accepts an include anywhere in this file. Every CLI command instead reads the
// file straight off disk through DecodeEvalConfig, which is strict, so the same
// document fails with `unknown key "$ref"`.
//
// Pinned rather than fixed because resolving it here is not just a matter of
// calling ResolveFileRefs: azd rebases only `project` and `instructions` path
// values (`includes.go:isPathKey`), so a declaration spliced in from another
// directory would keep a `source:` that resolves against the wrong base. Which
// keys an extension may have rebased is the azd team's call, and this test is
// what should fail when that answer arrives.
func TestRefInsideTheEvalConfigIsRejectedByTheCLIReader(t *testing.T) {
	withRef := []byte(`
evaluators:
  - $ref: ./evaluators/quality.yaml
evals:
  - name: nightly
`)

	_, err := DecodeEvalConfig(withRef, "azure.eval.yaml")

	require.Error(t, err, "`azd up` resolves this include; the CLI reader does not")
	assert.Contains(t, err.Error(), `unknown key "$ref"`,
		"the asymmetry has to be visible in the message a reader gets")

	// The spelling that does work today, for contrast.
	cfg, err := DecodeEvalConfig([]byte(`
evaluators:
  - name: quality
    source: ./evaluators/quality.json
evals:
  - name: nightly
`), "azure.eval.yaml")

	require.NoError(t, err)
	assert.Equal(t, "./evaluators/quality.json", cfg.Evaluators[0].Source)
}
