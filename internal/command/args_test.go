package command

import (
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
)

func TestLegacyFlagsPreserveValues(t *testing.T) {
	flags := pflag.NewFlagSet("import", pflag.ContinueOnError)
	mapping := flags.String("mapping", "", "mapping path")
	apply := flags.Bool("apply", false, "apply changes")
	require.NoError(
		t,
		flags.Parse(NormalizeArgs([]string{"-mapping", "-private.csv", "-apply", "--", "-untouched"}, flags)),
	)
	require.Equal(t, "-private.csv", *mapping)
	require.True(t, *apply)
	require.Equal(t, []string{"-untouched"}, flags.Args())
}
