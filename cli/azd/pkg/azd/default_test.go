// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package azd

import (
	"maps"
	"slices"
	"testing"

	"github.com/azure/azure-dev/cli/azd/pkg/azsdk/storage"
	"github.com/azure/azure-dev/cli/azd/pkg/environment"
	"github.com/azure/azure-dev/cli/azd/pkg/infra/provisioning"
	"github.com/azure/azure-dev/cli/azd/pkg/ioc"
	"github.com/azure/azure-dev/cli/azd/pkg/project"
	"github.com/azure/azure-dev/cli/azd/pkg/state"
	"github.com/stretchr/testify/require"
)

func Test_DefaultPlatform_IsEnabled(t *testing.T) {
	t.Run("Enabled", func(t *testing.T) {
		defaultPlatform := NewDefaultPlatform()
		require.True(t, defaultPlatform.IsEnabled())
	})
}

func Test_DefaultPlatform_ConfigureContainer(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		defaultPlatform := NewDefaultPlatform()
		container := ioc.NewNestedContainer(nil)
		err := defaultPlatform.ConfigureContainer(container)
		require.NoError(t, err)

		var provisionResolver provisioning.DefaultProviderResolver
		err = container.Resolve(&provisionResolver)
		require.NoError(t, err)
		require.NotNil(t, provisionResolver)

		expected := provisioning.Bicep
		actual, err := provisionResolver()
		require.NoError(t, err)
		require.Equal(t, expected, actual)
	})
}

// TestBuiltInProviderKindsMatchRegistrations keeps provisioning.BuiltInProviderKinds aligned with
// the providers azd actually registers. Callers use that list to decide a provider needs no
// extension, so a stale entry either consults the registry needlessly or skips a real requirement.
func TestBuiltInProviderKindsMatchRegistrations(t *testing.T) {
	require.ElementsMatch(
		t,
		slices.Collect(maps.Keys(provisionProviderMap)),
		provisioning.BuiltInProviderKinds(),
	)
}

// Test_DefaultPlatform_RemoteStateAccountConfig covers the conversion from the untyped
// `state.remoteState.config` map in azure.yaml to a storage.AccountConfig. The conversion goes
// through JSON and storage.AccountConfig declares no struct tags, so the camelCase keys documented
// in the azure.yaml schema bind only through Go's case-insensitive field matching.
func Test_DefaultPlatform_RemoteStateAccountConfig(t *testing.T) {
	tests := []struct {
		name          string
		remoteState   *state.RemoteConfig
		projectName   string
		expected      *storage.AccountConfig
		expectedError string
	}{
		{
			name:        "no remote state configured",
			remoteState: nil,
			projectName: "my-project",
			expected:    nil,
		},
		{
			name: "schema keys bind to the account config",
			remoteState: &state.RemoteConfig{
				Backend: string(environment.RemoteKindAzureBlobStorage),
				Config: map[string]any{
					"accountName":   "myaccount",
					"containerName": "mycontainer",
					"endpoint":      "blob.core.windows.net",
				},
			},
			projectName: "my-project",
			expected: &storage.AccountConfig{
				AccountName:   "myaccount",
				ContainerName: "mycontainer",
				Endpoint:      "blob.core.windows.net",
			},
		},
		{
			name: "container name defaults to the lowercased project name",
			remoteState: &state.RemoteConfig{
				Backend: string(environment.RemoteKindAzureBlobStorage),
				Config:  map[string]any{"accountName": "myaccount"},
			},
			// Blob container names must be lowercase, so a mixed-case project name is folded.
			projectName: "My-Project",
			expected: &storage.AccountConfig{
				AccountName:   "myaccount",
				ContainerName: "my-project",
			},
		},
		{
			name: "value of the wrong type is reported",
			remoteState: &state.RemoteConfig{
				Backend: string(environment.RemoteKindAzureBlobStorage),
				Config:  map[string]any{"accountName": 42},
			},
			projectName:   "my-project",
			expectedError: "unmarshalling remote state config",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// A fresh container per case: the account config is registered as a singleton.
			container := ioc.NewNestedContainer(nil)
			require.NoError(t, NewDefaultPlatform().ConfigureContainer(container))
			container.MustRegisterSingleton(func() *state.RemoteConfig { return test.remoteState })
			container.MustRegisterSingleton(func() *project.ProjectConfig {
				return &project.ProjectConfig{Name: test.projectName}
			})

			var accountConfig *storage.AccountConfig
			err := container.Resolve(&accountConfig)
			if test.expectedError != "" {
				require.ErrorContains(t, err, test.expectedError)
				return
			}

			require.NoError(t, err)
			require.Equal(t, test.expected, accountConfig)
		})
	}
}
