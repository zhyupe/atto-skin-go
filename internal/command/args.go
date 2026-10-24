package command

import (
	"strings"

	"github.com/spf13/pflag"
)

// NormalizeArgs preserves single-dash long flags without rewriting their values.
func NormalizeArgs(args []string, flags *pflag.FlagSet) []string {
	result := append([]string(nil), args...)
	valueNext := false
	for i, arg := range result {
		if valueNext {
			valueNext = false
			continue
		}
		if arg == "--" {
			break
		}
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		name, _, equals := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		flag := flags.Lookup(name)
		if flag == nil && name != "help" {
			continue
		}
		if !strings.HasPrefix(arg, "--") && len(name) > 1 {
			result[i] = "-" + arg
		}
		valueNext = flag != nil && flag.NoOptDefVal == "" && !equals
	}
	return result
}
