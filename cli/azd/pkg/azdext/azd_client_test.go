// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package azdext

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/metadata"
)

const (
	testTraceId = "68f1c4f4ef5346e69d7f196761d10c68"
	testSpanId  = "7fbdc197a52f4825"
)

func testTraceparent() string {
	return "00-" + testTraceId + "-" + testSpanId + "-01"
}

func Test_WithAccessToken_PropagatesEnvTraceContext(t *testing.T) {
	t.Setenv(TraceparentEnv, testTraceparent())
	t.Setenv(TracestateEnv, "vendor=value")

	md, ok := metadata.FromOutgoingContext(WithAccessToken(t.Context(), "token"))

	require.True(t, ok)
	require.Equal(t, []string{"token"}, md.Get("authorization"))
	require.Equal(t, []string{testTraceparent()}, md.Get(TraceparentKey))
	require.Equal(t, []string{"vendor=value"}, md.Get(TracestateKey))
}

func Test_WithAccessToken_PrefersContextSpan(t *testing.T) {
	t.Setenv(TraceparentEnv, testTraceparent())

	traceId, err := trace.TraceIDFromHex("11111111111111111111111111111111")
	require.NoError(t, err)
	spanId, err := trace.SpanIDFromHex("2222222222222222")
	require.NoError(t, err)

	ctx := trace.ContextWithSpanContext(t.Context(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceId,
		SpanID:     spanId,
		TraceFlags: trace.FlagsSampled,
	}))

	md, ok := metadata.FromOutgoingContext(WithAccessToken(ctx, "token"))

	require.True(t, ok)
	require.Equal(t, []string{
		"00-11111111111111111111111111111111-2222222222222222-01",
	}, md.Get(TraceparentKey))
}

func Test_WithAccessToken_OmitsMissingTraceContext(t *testing.T) {
	t.Setenv(TraceparentEnv, "")
	t.Setenv(TracestateEnv, "")

	md, ok := metadata.FromOutgoingContext(WithAccessToken(t.Context(), "token"))

	require.True(t, ok)
	require.Equal(t, []string{"token"}, md.Get("authorization"))
	require.Empty(t, md.Get(TraceparentKey))
	require.Empty(t, md.Get(TracestateKey))
}

func Test_IsLocalhostAddress(t *testing.T) {
	tests := []struct {
		name     string
		address  string
		expected bool
	}{
		{"localhost with port", "localhost:8080", true},
		{"127.0.0.1 with port", "127.0.0.1:3000", true},
		{"ipv6 loopback with port", "[::1]:8080", true},
		{"localhost without port", "localhost", true},
		{"127.0.0.1 without port", "127.0.0.1", true},
		{"external hostname", "example.com:8080", false},
		{"external IP", "192.168.1.1:8080", false},
		{"empty string", "", false},
		{"loopback IP 127.0.0.2", "127.0.0.2:8080", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isLocalhostAddress(tt.address)
			require.Equal(t, tt.expected, result)
		})
	}
}
