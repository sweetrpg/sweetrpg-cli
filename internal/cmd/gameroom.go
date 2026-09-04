package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/spf13/cobra"
	vo "github.com/sweetrpg/catalog-objects.go/vo"
	"github.com/sweetrpg/sweetrpg-cli/internal/auth"
	"github.com/sweetrpg/sweetrpg-cli/internal/client"
	"github.com/sweetrpg/sweetrpg-cli/internal/dtrpg"
)

var (
	flagGameRoomImportDryRun bool

	// gameRoomHTTPClient is a test seam for the raw game-room-api calls
	// below (no typed client exists for game-room-api in this repo yet).
	gameRoomHTTPClient = func() *http.Client { return http.DefaultClient }

	// platformSessionLoad is a test seam so tests supply a fake platform
	// session (for its Account/user-id) without touching the OS keychain.
	platformSessionLoad = func() (*auth.Session, error) { return (auth.KeyringStore{}).Load() }
)

// gameRoomImportChildren mirrors importChildren: kept here so tests can
// drive the command directly.
var gameRoomImportChildren = map[string]*cobra.Command{}

// newGameRoomCommand builds the game-room namespace. Only the DriveThruRPG
// import lives here today; it shares the top-level `dtrpg login` credential
// with `catalog import dtrpg library` rather than keeping its own.
func newGameRoomCommand() *cobra.Command {
	gr := &cobra.Command{
		Use:   "game-room",
		Short: "Manage your SweetRPG Game Room library",
	}
	imp := &cobra.Command{
		Use:   "import",
		Short: "Populate your Game Room library from external sources",
	}
	dtrpgCmd := &cobra.Command{
		Use:   "dtrpg",
		Short: "Add your owned DriveThruRPG products that already exist in the catalog",
		Long: "Match your DriveThruRPG library against volumes already in the SweetRPG catalog\n" +
			"and add every match to your own Game Room library. Never creates a catalog\n" +
			"record - a product with no matching volume is skipped and reported, not\n" +
			"imported. Uses the DriveThruRPG login stored by `sweetrpg dtrpg login`.",
		Args: cobra.NoArgs,
		RunE: runGameRoomImportDTRPG,
	}
	dtrpgCmd.Flags().BoolVar(&flagGameRoomImportDryRun, "dry-run", false,
		"fetch and match without adding any library entry")

	imp.AddCommand(dtrpgCmd)
	gr.AddCommand(imp)

	gameRoomImportChildren["dtrpg"] = dtrpgCmd
	return gr
}

// catalogVolumeMatch is the subset of a catalog volume the game-room import
// needs: enough to add a library entry ({volume_id, volume_title}).
type catalogVolumeMatch struct {
	id    string
	title string
}

// catalogVolumesByProductID pages every catalog volume once and indexes it
// by its dtrpg_product_id property, mirroring importedProductIDs in
// import_library.go but keeping the ID and title instead of just presence.
func catalogVolumesByProductID(ctx context.Context, c *client.Client) (map[string]catalogVolumeMatch, error) {
	out := map[string]catalogVolumeMatch{}
	for start := 0; ; start += scanPageSize {
		page, err := client.List[vo.VolumeVO](ctx, c, "volumes", client.ListOptions{Start: start, Limit: scanPageSize})
		if err != nil {
			return nil, err
		}
		for _, v := range page {
			for _, p := range v.Properties {
				if p.Name == dtrpg.PropProductID && p.Value != "" {
					out[p.Value] = catalogVolumeMatch{id: v.ID, title: v.Title}
				}
			}
		}
		if len(page) < scanPageSize {
			return out, nil
		}
	}
}

type gameRoomLibraryResponse struct {
	Entries []struct {
		VolumeID string `json:"volume_id"`
	} `json:"entries"`
}

// fetchGameRoomLibraryVolumeIDs returns the set of volume IDs already in the
// caller's own Game Room library, so already-present matches can be reported
// without an unnecessary write.
func fetchGameRoomLibraryVolumeIDs(ctx context.Context, baseURL, token, userID string) (map[string]bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, joinBaseAndPath(baseURL, "/users/"+userID+"/library"), nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := gameRoomHTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching your Game Room library: %w", err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("game-room-api returned %d fetching your library", resp.StatusCode)
	}
	var lib gameRoomLibraryResponse
	if err := json.NewDecoder(resp.Body).Decode(&lib); err != nil {
		return nil, fmt.Errorf("decoding your Game Room library: %w", err)
	}
	out := make(map[string]bool, len(lib.Entries))
	for _, e := range lib.Entries {
		out[e.VolumeID] = true
	}
	return out, nil
}

// addGameRoomLibraryEntry links one catalog volume into the caller's own
// Game Room library. The endpoint is owner-scoped to the caller's own user
// ID and idempotent, but the import checks presence itself first so it can
// report accurate added-vs-already-present counts.
func addGameRoomLibraryEntry(ctx context.Context, baseURL, token, userID, volumeID, volumeTitle string) error {
	body, err := json.Marshal(map[string]string{"volume_id": volumeID, "volume_title": volumeTitle})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, joinBaseAndPath(baseURL, "/users/"+userID+"/library/entries"), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := gameRoomHTTPClient().Do(req)
	if err != nil {
		return err
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("game-room-api returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}

type gameRoomImportSummary struct {
	added               int
	alreadyPresent      int
	skippedNotInCatalog int
	failed              int
	skippedTitles       []string
	failures            []string
}

func runGameRoomImportDTRPG(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	if err := requirePlatformSession(); err != nil {
		return err
	}
	sess, err := platformSessionLoad()
	if err != nil {
		return authExit(err)
	}
	userID := sess.Account

	appKey, err := dtrpgKeyStore().LoadKey()
	if err != nil {
		return err
	}
	dtrpgSession, err := buildDTRPGClient(ctx, appKey)
	if err != nil {
		return err
	}
	lib, err := dtrpgSession.FetchLibrary(ctx, 0, nil)
	if err != nil {
		return fmt.Errorf("fetching DriveThruRPG library: %w", err)
	}
	products := dtrpg.MapProducts(lib)

	catalogClient, err := buildAnonClient()
	if err != nil {
		return err
	}
	volumesByProductID, err := catalogVolumesByProductID(ctx, catalogClient)
	if err != nil {
		return err
	}

	baseURL, tokens, err := resolveAPIRequest("game-room")
	if err != nil {
		return err
	}
	token, err := tokens(ctx)
	if err != nil {
		return authExit(err)
	}

	existing, err := fetchGameRoomLibraryVolumeIDs(ctx, baseURL, token, userID)
	if err != nil {
		return err
	}

	var s gameRoomImportSummary
	for _, p := range products {
		match, ok := volumesByProductID[p.ProductID]
		if !ok {
			s.skippedNotInCatalog++
			s.skippedTitles = append(s.skippedTitles, p.Title)
			continue
		}
		if existing[match.id] {
			s.alreadyPresent++
			continue
		}
		if flagGameRoomImportDryRun {
			s.added++
			continue
		}
		if err := addGameRoomLibraryEntry(ctx, baseURL, token, userID, match.id, match.title); err != nil {
			s.failed++
			s.failures = append(s.failures, fmt.Sprintf("%s: %v", p.Title, err))
			continue
		}
		s.added++
	}

	printGameRoomImportSummary(cmd, s, flagGameRoomImportDryRun)
	if s.failed > 0 {
		return &ExitError{Code: 1, Err: fmt.Errorf("%d product(s) failed to add", s.failed)}
	}
	return nil
}

func printGameRoomImportSummary(cmd *cobra.Command, s gameRoomImportSummary, dryRun bool) {
	suffix := ""
	if dryRun {
		suffix = " (dry run, nothing written)"
	}
	cmd.Printf("Done: %d added, %d already present, %d skipped (not in catalog)%s\n",
		s.added, s.alreadyPresent, s.skippedNotInCatalog, suffix)
	if len(s.skippedTitles) > 0 {
		cmd.Println("  skipped (not in catalog):")
		for _, title := range s.skippedTitles {
			cmd.Printf("    - %s\n", title)
		}
	}
	for _, f := range s.failures {
		cmd.Printf("  failed: %s\n", f)
	}
}
