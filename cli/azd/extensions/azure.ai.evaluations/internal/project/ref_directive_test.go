// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package project

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `$ref` is a directive, and the strict decoder never sees one.
//
// Resolution happens in LoadEvalConfig, which strips the directive once the
// file it names has been spliced in. DecodeEvalConfig is the decoder underneath
// that, so a `$ref` reaching it means resolution was skipped — and reporting it
// as a mistyped key is the right answer, because a configuration that still
// carries the directive has not been resolved and cannot deploy either.
//
// This used to be the whole story: the service target resolved and this path did
// not, so the same file deployed cleanly and then failed every CLI command.
// TestRefResolvesOnTheCLIPathToo covers the resolved route.
func TestTheStrictDecoderNeverSeesARefDirective(t *testing.T) {
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
