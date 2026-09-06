package cmdutil

import (
	"fmt"

	"github.com/spf13/cobra"
)

// ParseOutputFormat maps the --output flag onto the printer's format.
//
// One copy of a mapping that was written out at every list and info command,
// and had already drifted: the info commands refused an unknown value while
// the list commands quietly printed a table, so "--output yaml" produced
// different behavior depending on which subcommand it was passed to. An
// empty value means the default, table.
func ParseOutputFormat(flag string) (OutputFormat, error) {
	switch flag {
	case "json":
		return OutputFormatJSON, nil
	case "", "table":
		return OutputFormatTable, nil
	default:
		return "", fmt.Errorf("unknown --%s value %q: expected \"table\" or \"json\"", FlagOutput, flag)
	}
}

// OutputFormatFrom reads --output and parses it in one step.
//
// Reading and parsing were separate, and every caller parsed at the point it
// printed — after the daemon round-trip. So "--output yaml" made the request,
// waited for it, and only then refused the flag. Taking both together lets a
// handler settle the format before it does any work.
func OutputFormatFrom(cmd *cobra.Command) (OutputFormat, error) {
	flag, _ := cmd.Flags().GetString(FlagOutput)
	return ParseOutputFormat(flag)
}
