// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package azd

import (
	"maps"
	"slices"
	"testing"

	"github.com/azure/azure-dev/cli/azd/pkg/infra/provisioning"
	"github.com/azure/azure-dev/cli/azd/pkg/ioc"
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
