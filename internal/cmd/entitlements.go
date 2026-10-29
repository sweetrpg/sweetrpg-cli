package cmd

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/sweetrpg/sweetrpg-cli/internal/entitlements"
)

var (
	flagEntitlementsSeedFile   string
	flagEntitlementsSeedPrices string
	flagEntitlementsSeedDryRun bool
)

// newEntitlementsCommand builds the entitlements namespace. Only `seed` lives here today.
func newEntitlementsCommand() *cobra.Command {
	ent := &cobra.Command{
		Use:   "entitlements",
		Short: "Manage the SweetRPG entitlements catalog",
	}
	seed := &cobra.Command{
		Use:   "seed --file <seed.json> [--prices <prices.json>] [--dry-run]",
		Short: "Load a catalog seed (and optional Stripe price overlay) through the admin API",
		Long: "Load keys, tiers, the default tier, and products from a seed file through the\n" +
			"entitlements admin API, authenticated as you (the admin role is checked server-side).\n\n" +
			"Create-only: an existing key, tier, or product is left exactly as it is, and an\n" +
			"already-set default tier is not changed. --prices applies a per-environment overlay\n" +
			"of Stripe price IDs: a price is set only where the product has none, and a different\n" +
			"existing price is reported as skipped. The seed and price files live in the\n" +
			"entitlements-api repo (seed/initiative.json, seed/prices.dev.json).\n\n" +
			"  sweetrpg entitlements seed --file seed/initiative.json --prices seed/prices.dev.json --dry-run",
		Args: cobra.NoArgs,
		RunE: runEntitlementsSeed,
	}
	seed.Flags().StringVar(&flagEntitlementsSeedFile, "file", "", "seed file (required)")
	seed.Flags().StringVar(&flagEntitlementsSeedPrices, "prices", "", "per-environment Stripe price overlay")
	seed.Flags().BoolVar(&flagEntitlementsSeedDryRun, "dry-run", false, "report what would change without writing")
	_ = seed.MarkFlagRequired("file")
	ent.AddCommand(seed)
	return ent
}

func runEntitlementsSeed(cmd *cobra.Command, _ []string) error {
	if flagCurl {
		return usageErr("--curl is not supported for seed: it makes requests that depend on earlier responses")
	}
	doc, overlay, err := readSeedFiles(flagEntitlementsSeedFile, flagEntitlementsSeedPrices)
	if err != nil {
		return usageErr("%v", err)
	}

	baseURL, tokens, err := resolveAPIRequest("entitlements")
	if err != nil {
		return err
	}
	token, err := tokens(cmd.Context())
	if err != nil {
		return authExit(err)
	}

	client := &entitlements.Client{BaseURL: baseURL, Token: token, HTTP: &http.Client{Timeout: 30 * time.Second}}
	report, err := client.Load(cmd.Context(), doc, flagEntitlementsSeedDryRun)
	if err == nil && overlay != nil {
		err = client.ApplyPrices(cmd.Context(), overlay, flagEntitlementsSeedDryRun, report)
	}
	for _, l := range report.Created {
		cmd.Println("created ", l)
	}
	for _, l := range report.Existing {
		cmd.Println("exists  ", l)
	}
	for _, l := range report.Skipped {
		cmd.Println("skipped ", l)
	}
	return err
}

// readSeedFiles parses the seed file and, when given, the price overlay. Problems here are the
// caller's input, reported as usage errors.
func readSeedFiles(seedPath, pricesPath string) (*entitlements.Doc, *entitlements.Prices, error) {
	raw, err := os.ReadFile(seedPath)
	if err != nil {
		return nil, nil, err
	}
	doc, err := entitlements.Parse(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", seedPath, err)
	}
	if pricesPath == "" {
		return doc, nil, nil
	}
	raw, err = os.ReadFile(pricesPath)
	if err != nil {
		return nil, nil, err
	}
	overlay, err := entitlements.ParsePrices(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", pricesPath, err)
	}
	return doc, overlay, nil
}
