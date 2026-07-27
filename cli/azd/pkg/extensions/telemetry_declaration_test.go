// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package extensions

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const testExtensionId = "azure.ai.agents"

func Test_ValidateTelemetryDeclarations_Valid(t *testing.T) {
	t.Parallel()

	issues := ValidateTelemetryDeclarations(testExtensionId, []TelemetryFieldDeclaration{
		{
			Key:           "ext.azure.ai.agents.deploy.mode",
			AllowedValues: []string{"code", "container", "byo_image"},
		},
		{
			Key:           "ext.azure.ai.agents.deploy-target",
			AllowedValues: []string{"aca"},
		},
	})

	require.Empty(t, issues)
}

func Test_ValidateTelemetryDeclarations_Empty(t *testing.T) {
	t.Parallel()

	require.Empty(t, ValidateTelemetryDeclarations(testExtensionId, nil))
}

func Test_ValidateTelemetryDeclarations_RejectsBadKeys(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"missing key":       "",
		"core namespace":    "agent.deploy.mode",
		"other extension":   "ext.contoso.tools.deploy.mode",
		"prefix only":       "ext.azure.ai.agents.",
		"uppercase segment": "ext.azure.ai.agents.Mode",
		"empty segment":     "ext.azure.ai.agents..mode",
		"too long":          "ext.azure.ai.agents." + strings.Repeat("m", MaxTelemetryKeyLength),
	}

	for name, key := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			issues := ValidateTelemetryDeclarations(testExtensionId, []TelemetryFieldDeclaration{
				{Key: key, AllowedValues: []string{"code"}},
			})

			require.NotEmpty(t, issues)
		})
	}
}

func Test_ValidateTelemetryDeclarations_RejectsBadValues(t *testing.T) {
	t.Parallel()

	tests := map[string][]string{
		"no values":        {},
		"empty value":      {""},
		"registry name":    {"byo_image:myregistry.azurecr.io/foo"},
		"path":             {"images/foo"},
		"uppercase":        {"Code"},
		"duplicate":        {"code", "code"},
		"too long":         {strings.Repeat("v", MaxTelemetryValueLength+1)},
		"too many entries": make([]string, MaxTelemetryAllowedValues+1),
	}

	for name, values := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			issues := ValidateTelemetryDeclarations(testExtensionId, []TelemetryFieldDeclaration{
				{Key: "ext.azure.ai.agents.deploy.mode", AllowedValues: values},
			})

			require.NotEmpty(t, issues)
		})
	}
}

func Test_ValidateTelemetryDeclarations_RejectsOversizedSets(t *testing.T) {
	t.Parallel()

	declarations := make([]TelemetryFieldDeclaration, 0, MaxTelemetryFields+1)
	for i := range MaxTelemetryFields + 1 {
		declarations = append(declarations, TelemetryFieldDeclaration{
			Key:           "ext.azure.ai.agents.field" + string(rune('a'+i)),
			AllowedValues: []string{"code"},
		})
	}

	require.NotEmpty(t, ValidateTelemetryDeclarations(testExtensionId, declarations))
}

func Test_ValidateTelemetryDeclarations_RejectsDuplicateKeys(t *testing.T) {
	t.Parallel()

	issues := ValidateTelemetryDeclarations(testExtensionId, []TelemetryFieldDeclaration{
		{Key: "ext.azure.ai.agents.deploy.mode", AllowedValues: []string{"code"}},
		{Key: "ext.azure.ai.agents.deploy.mode", AllowedValues: []string{"container"}},
	})

	require.NotEmpty(t, issues)
}

func Test_ValidateRegistry_TelemetryRequiresCapability(t *testing.T) {
	t.Parallel()

	metadata := []*ExtensionMetadata{
		{
			Id:          testExtensionId,
			DisplayName: "Agents",
			Description: "Test extension",
			Versions: []ExtensionVersion{
				{
					Version: "1.0.0",
					Telemetry: []TelemetryFieldDeclaration{
						{
							Key:           "ext.azure.ai.agents.deploy.mode",
							AllowedValues: []string{"code"},
						},
					},
					Artifacts: map[string]ExtensionArtifact{
						"linux/amd64": {
							URL:      "https://example.com/agents.zip",
							Checksum: ExtensionChecksum{Algorithm: "sha256", Value: "abc"},
						},
					},
				},
			},
		},
	}

	result := ValidateExtensions(metadata, false)

	require.False(t, result.Valid)
	require.Contains(t, strings.Join(collectMessages(result), "\n"), "telemetry")
}

func Test_ValidateRegistry_TelemetryValidWithCapability(t *testing.T) {
	t.Parallel()

	metadata := []*ExtensionMetadata{
		{
			Id:          testExtensionId,
			DisplayName: "Agents",
			Description: "Test extension",
			Versions: []ExtensionVersion{
				{
					Version:      "1.0.0",
					Capabilities: []CapabilityType{TelemetryCapability},
					Telemetry: []TelemetryFieldDeclaration{
						{
							Key:           "ext.azure.ai.agents.deploy.mode",
							AllowedValues: []string{"code"},
						},
					},
					Artifacts: map[string]ExtensionArtifact{
						"linux/amd64": {
							URL:      "https://example.com/agents.zip",
							Checksum: ExtensionChecksum{Algorithm: "sha256", Value: "abc"},
						},
					},
				},
			},
		},
	}

	result := ValidateExtensions(metadata, false)

	require.True(t, result.Valid, strings.Join(collectMessages(result), "\n"))
}

func collectMessages(result *RegistryValidationResult) []string {
	messages := []string{}

	for _, issue := range result.Issues {
		messages = append(messages, issue.Message)
	}

	for _, extension := range result.Extensions {
		for _, issue := range extension.Issues {
			messages = append(messages, issue.Message)
		}
	}

	return messages
}
