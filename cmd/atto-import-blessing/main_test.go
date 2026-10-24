package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLegacyAndCobraHelp(t *testing.T) {
	for _, flag := range []string{"-help", "--help", "-h"} {
		var out bytes.Buffer
		require.NoError(t, run(t.Context(), []string{flag}, &out))
		require.Contains(t, out.String(), "history-only")
	}
}

func TestFlagsAreValidatedBeforeConnecting(t *testing.T) {
	var out bytes.Buffer
	err := run(t.Context(), []string{"-export-mapping", "mapping.csv", "-history-only"}, &out)
	require.ErrorContains(t, err, "cannot be combined")
	err = run(
		t.Context(),
		[]string{
			"--mapping",
			"mapping.csv",
			"--data-dir",
			"data",
			"--oidc-issuer",
			"https://id.example",
			"--timeout",
			"0s",
		},
		&out,
	)
	require.ErrorContains(t, err, "-timeout must be positive")
}
