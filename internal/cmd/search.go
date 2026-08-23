package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// searchCommand is kept as a var so tests can drive it without re-finding
// the command tree.
var searchCommand *cobra.Command

func newSearchCommand() *cobra.Command {
	searchCommand = &cobra.Command{
		Use:   "search <type> <query>",
		Short: "List catalog records matching a case-insensitive partial name",
		Long: `Search one catalog entity type by case-insensitive partial name or title
match. Prints each hit's ID and display name so IDs can feed other commands.
Exact matches are preferred; otherwise every record containing the query is listed.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ops, err := lookupEntity(args[0])
			if err != nil {
				return usageErr("%v", err)
			}
			c, err := buildAnonClient()
			if err != nil {
				return err
			}
			candidates, err := ops.find(cmd.Context(), c, args[1])
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(candidates) == 0 {
				fmt.Fprintf(out, "no %s matches for %q\n", ops.spec.Name, args[1])
				return nil
			}
			for _, cand := range candidates {
				detail := ""
				if cand.Detail != "" {
					detail = " " + cand.Detail
				}
				fmt.Fprintf(out, "%s\t%s%s\n", cand.ID, cand.Label, detail)
			}
			fmt.Fprintf(out, "\n%d %s match(es)\n", len(candidates), ops.spec.Name)
			return nil
		},
	}
	return searchCommand
}
