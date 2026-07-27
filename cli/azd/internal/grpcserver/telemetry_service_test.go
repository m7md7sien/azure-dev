// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package grpcserver

import (
	"strings"
	"testing"

	"github.com/azure/azure-dev/cli/azd/pkg/azdext"
	"github.com/azure/azure-dev/cli/azd/pkg/extensions"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const testTelemetryKey = "ext.azd.internal.telemetry.deploy.mode"

// stubExtensionLookup returns a fixed installed extension record, or a
// not-found error when the requested id does not match.
type stubExtensionLookup struct {
	extension *extensions.Extension
}

func (s stubExtensionLookup) GetInstalled(
	options extensions.FilterOptions,
) (*extensions.Extension, error) {
	if s.extension == nil || s.extension.Id != options.Id {
		return nil, extensions.ErrInstalledExtensionNotFound
	}

	return s.extension, nil
}

func testExtension() *extensions.Extension {
	return &extensions.Extension{
		Id:      "azd.internal.telemetry",
		Version: "1.0.0",
		Source:  extensions.MainRegistryName,
		Capabilities: []extensions.CapabilityType{
			extensions.TelemetryCapability,
		},
		Telemetry: []extensions.TelemetryFieldDeclaration{
			{
				Key:           testTelemetryKey,
				AllowedValues: []string{"code", "container"},
			},
		},
	}
}

// callWith runs the handler as the given extension, mirroring how the auth
// interceptor injects host-signed claims.
func callWith(
	t *testing.T,
	extension *extensions.Extension,
	req *azdext.ReportUsageAttributeRequest,
) (*azdext.ReportUsageAttributeResponse, error) {
	t.Helper()

	service := newTelemetryService(stubExtensionLookup{extension})
	ctx := extensions.WithClaimsContext(t.Context(), &extensions.ExtensionClaims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: extension.Id},
		Capabilities:     extension.Capabilities,
		Source:           extension.Source,
	})

	return service.ReportUsageAttribute(ctx, req)
}

func requireCode(t *testing.T, err error, expected codes.Code) {
	t.Helper()

	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, expected, st.Code())
}

func Test_TelemetryService_AcceptsDeclaredValue(t *testing.T) {
	t.Parallel()

	resp, err := callWith(t, testExtension(), &azdext.ReportUsageAttributeRequest{
		Key:   testTelemetryKey,
		Value: "container",
	})

	require.NoError(t, err)
	require.True(t, resp.Accepted)
}

func Test_TelemetryService_RequiresClaims(t *testing.T) {
	t.Parallel()

	service := newTelemetryService(stubExtensionLookup{testExtension()})
	_, err := service.ReportUsageAttribute(t.Context(), &azdext.ReportUsageAttributeRequest{
		Key:   testTelemetryKey,
		Value: "code",
	})

	requireCode(t, err, codes.Unauthenticated)
}

func Test_TelemetryService_RejectsInvalidRequests(t *testing.T) {
	t.Parallel()

	tests := map[string]*azdext.ReportUsageAttributeRequest{
		"nil":         nil,
		"empty key":   {Key: "", Value: "code"},
		"empty value": {Key: testTelemetryKey, Value: ""},
		"long key": {
			Key:   strings.Repeat("k", extensions.MaxTelemetryKeyLength+1),
			Value: "code",
		},
		"long value": {
			Key:   testTelemetryKey,
			Value: strings.Repeat("v", extensions.MaxTelemetryValueLength+1),
		},
	}

	for name, req := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := callWith(t, testExtension(), req)
			requireCode(t, err, codes.InvalidArgument)
		})
	}
}

func Test_TelemetryService_RejectsUnofficialSource(t *testing.T) {
	t.Parallel()

	extension := testExtension()
	extension.Source = "dev"

	_, err := callWith(t, extension, &azdext.ReportUsageAttributeRequest{
		Key:   testTelemetryKey,
		Value: "code",
	})

	requireCode(t, err, codes.PermissionDenied)
}

func Test_TelemetryService_RejectsMissingSource(t *testing.T) {
	t.Parallel()

	// An install predating source tracking must fail closed rather than be
	// treated as coming from the official registry.
	extension := testExtension()
	extension.Source = ""

	_, err := callWith(t, extension, &azdext.ReportUsageAttributeRequest{
		Key:   testTelemetryKey,
		Value: "code",
	})

	requireCode(t, err, codes.PermissionDenied)
}

func Test_TelemetryService_RejectsMissingCapability(t *testing.T) {
	t.Parallel()

	extension := testExtension()
	extension.Capabilities = []extensions.CapabilityType{
		extensions.CustomCommandCapability,
	}

	_, err := callWith(t, extension, &azdext.ReportUsageAttributeRequest{
		Key:   testTelemetryKey,
		Value: "code",
	})

	requireCode(t, err, codes.PermissionDenied)
}

func Test_TelemetryService_RejectsUninstalledExtension(t *testing.T) {
	t.Parallel()

	service := newTelemetryService(stubExtensionLookup{})
	ctx := extensions.WithClaimsContext(t.Context(), &extensions.ExtensionClaims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: "azd.internal.telemetry"},
		Capabilities:     []extensions.CapabilityType{extensions.TelemetryCapability},
		Source:           extensions.MainRegistryName,
	})

	_, err := service.ReportUsageAttribute(ctx, &azdext.ReportUsageAttributeRequest{
		Key:   testTelemetryKey,
		Value: "code",
	})

	requireCode(t, err, codes.PermissionDenied)
}

func Test_TelemetryService_RejectsUndeclaredKeyOrValue(t *testing.T) {
	t.Parallel()

	tests := map[string]*azdext.ReportUsageAttributeRequest{
		"undeclared key":   {Key: "ext.azd.internal.telemetry.other", Value: "code"},
		"undeclared value": {Key: testTelemetryKey, Value: "byo_image"},
		"core key":         {Key: "agent.deploy.mode", Value: "code"},
	}

	for name, req := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := callWith(t, testExtension(), req)
			requireCode(t, err, codes.InvalidArgument)
		})
	}
}

func Test_TelemetryService_RejectsTamperedDeclaration(t *testing.T) {
	t.Parallel()

	// The installed record lives in user config, so a locally widened
	// declaration must be rejected instead of trusted.
	extension := testExtension()
	extension.Telemetry = []extensions.TelemetryFieldDeclaration{
		{
			Key:           "agent.deploy.mode",
			AllowedValues: []string{"code"},
		},
	}

	_, err := callWith(t, extension, &azdext.ReportUsageAttributeRequest{
		Key:   "agent.deploy.mode",
		Value: "code",
	})

	requireCode(t, err, codes.FailedPrecondition)
}
