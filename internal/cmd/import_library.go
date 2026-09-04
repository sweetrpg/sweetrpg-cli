package cmd

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/spf13/cobra"
	vo "github.com/sweetrpg/catalog-objects.go/vo"
	modelcore "github.com/sweetrpg/model-core.go/vo"
	"github.com/sweetrpg/sweetrpg-cli/internal/auth"
	"github.com/sweetrpg/sweetrpg-cli/internal/client"
	"github.com/sweetrpg/sweetrpg-cli/internal/dtrpg"
)

var (
	flagImportDryRun   bool
	flagImportArchived bool
	flagImportPageSize uint32
	flagImportQuiet    bool
	flagImportVerbose  bool

	// requirePlatformSession is a seam so tests skip the keychain check.
	requirePlatformSession = defaultRequirePlatformSession

	// fetchCover is a seam so tests point cover downloads at a fixture server.
	fetchCover = func(ctx context.Context, url string) (*dtrpg.Cover, error) {
		return dtrpg.FetchCover(ctx, http.DefaultClient, url)
	}
)

// scanPageSize bounds each catalog-api list page during the dedup and
// publisher scans. A personal library is a few hundred volumes, so this is a
// handful of requests.
const scanPageSize = 500

// logNormal prints routine progress output (one line per volume processed,
// plan samples, cover attach/skip detail) - suppressed only by --quiet.
// Failures and the final summary counts are never gated by this; they print
// regardless of --quiet.
func logNormal(cmd *cobra.Command, format string, args ...any) {
	if flagImportQuiet {
		return
	}
	cmd.Printf(format, args...)
}

// logVerbose prints extra detail (DTRPG page fetch progress, publisher
// resolution decisions, cover attach successes) - only with --verbose.
// --verbose implies --quiet does not suppress it.
func logVerbose(cmd *cobra.Command, format string, args ...any) {
	if !flagImportVerbose {
		return
	}
	cmd.Printf(format, args...)
}

// defaultRequirePlatformSession refuses the import (exit 3) when no platform
// session is stored, before any DriveThruRPG call is made.
func defaultRequirePlatformSession() error {
	if _, err := resolveAuthConfig(); err != nil {
		return err
	}
	if _, err := (auth.KeyringStore{}).Load(); err != nil {
		if auth.IsAuthRequired(err) {
			return &ExitError{Code: 3, Err: fmt.Errorf("not logged in to the platform: run 'sweetrpg auth login'")}
		}
		return err
	}
	return nil
}

func runDTRPGLibrary(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	if err := requirePlatformSession(); err != nil {
		return err
	}
	appKey, err := dtrpgKeyStore().LoadKey()
	if err != nil {
		return err
	}

	session, err := buildDTRPGClient(ctx, appKey)
	if err != nil {
		return err
	}
	logNormal(cmd, "Fetching DriveThruRPG library...\n")
	lib, err := session.FetchLibrary(ctx, flagImportPageSize, func(page, fetched int) {
		logVerbose(cmd, "  fetched page %d (%d products so far)\n", page, fetched)
	})
	if err != nil {
		return fmt.Errorf("fetching DriveThruRPG library: %w", err)
	}
	logNormal(cmd, "Fetched %d products from DriveThruRPG.\n", len(lib.Products))
	products := dtrpg.MapProducts(lib)

	c, err := buildAPIClient()
	if err != nil {
		return err
	}
	known, err := importedProductIDs(ctx, c)
	if err != nil {
		return writeErr(err)
	}

	plan := planImport(products, known, flagImportArchived)
	printPlan(cmd, plan)

	if flagImportDryRun {
		cmd.Println("Dry run: no records created.")
		return nil
	}

	summary := executeImport(ctx, cmd, c, plan.toImport)
	printSummary(cmd, plan, summary)
	if summary.failed > 0 {
		return &ExitError{Code: 1, Err: fmt.Errorf("%d product(s) failed to import", summary.failed)}
	}
	return nil
}

// importedProductIDs pages the volume list once and collects every
// dtrpg_product_id property value already in the catalog.
func importedProductIDs(ctx context.Context, c *client.Client) (map[string]bool, error) {
	out := map[string]bool{}
	for start := 0; ; start += scanPageSize {
		page, err := client.List[vo.VolumeVO](ctx, c, "volumes", client.ListOptions{Start: start, Limit: scanPageSize})
		if err != nil {
			return nil, err
		}
		for _, v := range page {
			for _, p := range v.Properties {
				if p.Name == dtrpg.PropProductID && p.Value != "" {
					out[p.Value] = true
				}
			}
		}
		if len(page) < scanPageSize {
			return out, nil
		}
	}
}

type importPlan struct {
	toImport        []dtrpg.Product
	alreadyImported []dtrpg.Product
	skippedArchived []dtrpg.Product
}

// planImport classifies each mapped product. An already-imported product is
// reported as such even when archived, so re-runs converge.
func planImport(products []dtrpg.Product, known map[string]bool, includeArchived bool) importPlan {
	var p importPlan
	for _, prod := range products {
		switch {
		case known[prod.ProductID]:
			p.alreadyImported = append(p.alreadyImported, prod)
		case prod.Archived && !includeArchived:
			p.skippedArchived = append(p.skippedArchived, prod)
		default:
			p.toImport = append(p.toImport, prod)
		}
	}
	return p
}

type importSummary struct {
	imported       int
	failed         int
	failures       []string
	coversAttached int
	coversSkipped  int
}

func executeImport(ctx context.Context, cmd *cobra.Command, c *client.Client, products []dtrpg.Product) importSummary {
	resolver := newPublisherResolver(cmd, c)
	assets, assetsErr := buildAssetsClient()
	if assetsErr != nil {
		cmd.Printf("  covers disabled: %v\n", assetsErr)
	}

	var s importSummary
	for _, prod := range products {
		logNormal(cmd, "  processing %s...\n", prod.Title)

		volumeID, err := importOne(ctx, c, resolver, prod)
		if err != nil {
			s.failed++
			s.failures = append(s.failures, fmt.Sprintf("%s: %v", prod.Title, err))
			cmd.Printf("  failed   %s (%v)\n", prod.Title, err)
			continue
		}
		s.imported++
		logNormal(cmd, "  imported %s\n", prod.Title)

		if assetsErr != nil {
			s.coversSkipped++
			continue
		}
		if attachCover(ctx, cmd, c, assets, volumeID, prod) {
			s.coversAttached++
			logVerbose(cmd, "  cover attached for %s\n", prod.Title)
		} else {
			s.coversSkipped++
		}
	}
	return s
}

// importOne creates the volume and, when the product names a publisher, links
// the resolved publisher record to it. Returns the created volume's ID.
func importOne(ctx context.Context, c *client.Client, resolver *publisherResolver, prod dtrpg.Product) (string, error) {
	var publisherID string
	if prod.PublisherName != "" {
		id, err := resolver.resolve(ctx, prod.PublisherName)
		if err != nil {
			return "", fmt.Errorf("resolving publisher %q: %w", prod.PublisherName, err)
		}
		publisherID = id
	}

	fields := client.VolumeCreateFields{
		Title:       prod.Volume.Title,
		Description: prod.Volume.Description,
		Tags:        tagNames(prod.Volume.Tags),
		Properties:  prod.Volume.Properties,
	}
	created, err := client.CreateVolume(ctx, c, fields)
	if err != nil {
		return "", err
	}
	if publisherID != "" {
		if _, _, err := client.Patch[vo.VolumeVO](ctx, c, "volumes", created.ID, map[string]any{"publisherIds": []string{publisherID}}); err != nil {
			return created.ID, fmt.Errorf("linking publisher: %w", err)
		}
	}
	return created.ID, nil
}

// tagNames reduces DTRPG-mapped tags to the plain names POST /volumes and
// PATCH /volumes/:id both expect on the wire; the {name,value} TagVO shape is
// a read-side convenience only.
func tagNames(tags []modelcore.TagVO) []string {
	if len(tags) == 0 {
		return nil
	}
	names := make([]string, len(tags))
	for i, t := range tags {
		names[i] = t.Name
	}
	return names
}

// attachCover fetches the product's cover from DriveThruRPG and stores it as
// the volume's own asset. Any failure - no cover, fetch error, upload error -
// is a warning: the volume already imported successfully and stays imported.
func attachCover(ctx context.Context, cmd *cobra.Command, c *client.Client, assets *client.AssetsClient, volumeID string, prod dtrpg.Product) bool {
	cover, err := fetchCover(ctx, prod.CoverURL)
	if err != nil {
		logNormal(cmd, "  cover skipped for %s: %v\n", prod.Title, err)
		return false
	}
	if _, _, err := client.SetVolumeCover(ctx, c, assets, volumeID, cover.Data, cover.ContentType); err != nil {
		logNormal(cmd, "  cover skipped for %s: %v\n", prod.Title, err)
		return false
	}
	return true
}

// publisherResolver matches publisher names case-insensitively against
// existing records, loading the full list once and creating a record on the
// first miss for a name.
type publisherResolver struct {
	c      *client.Client
	cmd    *cobra.Command
	byName map[string]string
	loaded bool
}

func newPublisherResolver(cmd *cobra.Command, c *client.Client) *publisherResolver {
	return &publisherResolver{c: c, cmd: cmd, byName: map[string]string{}}
}

func (r *publisherResolver) load(ctx context.Context) error {
	if r.loaded {
		return nil
	}
	for start := 0; ; start += scanPageSize {
		page, err := client.List[vo.PublisherVO](ctx, r.c, "publishers", client.ListOptions{Start: start, Limit: scanPageSize})
		if err != nil {
			return err
		}
		for _, p := range page {
			if name := strings.ToLower(strings.TrimSpace(p.Name)); name != "" {
				r.byName[name] = p.ID
			}
		}
		if len(page) < scanPageSize {
			break
		}
	}
	r.loaded = true
	return nil
}

func (r *publisherResolver) resolve(ctx context.Context, name string) (string, error) {
	if err := r.load(ctx); err != nil {
		return "", err
	}
	key := strings.ToLower(strings.TrimSpace(name))
	if id, ok := r.byName[key]; ok {
		logVerbose(r.cmd, "  publisher %q resolved to existing record %s\n", name, id)
		return id, nil
	}
	created, err := client.Create[vo.PublisherVO](ctx, r.c, "publishers", &vo.PublisherVO{Name: name})
	if err != nil {
		return "", err
	}
	r.byName[key] = created.ID
	logVerbose(r.cmd, "  publisher %q created as new record %s\n", name, created.ID)
	return created.ID, nil
}

func printPlan(cmd *cobra.Command, plan importPlan) {
	cmd.Printf("Plan: %d to import, %d already imported, %d skipped (archived)\n",
		len(plan.toImport), len(plan.alreadyImported), len(plan.skippedArchived))
	printSample(cmd, "to import", plan.toImport)
	printSample(cmd, "skipped (archived)", plan.skippedArchived)
}

func printSample(cmd *cobra.Command, label string, products []dtrpg.Product) {
	const max = 10
	if len(products) == 0 {
		return
	}
	logNormal(cmd, "  %s:\n", label)
	for i, p := range products {
		if i == max {
			logNormal(cmd, "    ... and %d more\n", len(products)-max)
			break
		}
		logNormal(cmd, "    - %s\n", p.Title)
	}
}

func printSummary(cmd *cobra.Command, plan importPlan, s importSummary) {
	cmd.Printf("Done: %d imported, %d already imported, %d skipped (archived), %d failed\n",
		s.imported, len(plan.alreadyImported), len(plan.skippedArchived), s.failed)
	cmd.Printf("Covers: %d attached, %d skipped\n", s.coversAttached, s.coversSkipped)
	for _, f := range s.failures {
		cmd.Printf("  failed: %s\n", f)
	}
}
