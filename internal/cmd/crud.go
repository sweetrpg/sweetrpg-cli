package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/sweetrpg/sweetrpg-cli/internal/auth"
	"github.com/sweetrpg/sweetrpg-cli/internal/client"
	"github.com/sweetrpg/sweetrpg-cli/internal/config"
)

var (
	flagAPIURL       string
	flagAssetsWebURL string

	// buildAPIClient and buildAnonClient are vars so tests can point commands
	// at a fixture server. Reads (view) hit unauthenticated endpoints and use
	// the anonymous builder; everything else requires a session.
	buildAPIClient  = func() (*client.Client, error) { return newAPIClient(true) }
	buildAnonClient = func() (*client.Client, error) { return newAPIClient(false) }
)

// Generated per-entity children are kept here so tests can drive them.
var (
	addChildren    = map[string]*cobra.Command{}
	editChildren   = map[string]*cobra.Command{}
	viewChildren   = map[string]*cobra.Command{}
	deleteChildren = map[string]*cobra.Command{}
)

func sortedEntityNames() []string {
	names := make([]string, 0, len(entityRegistry))
	for name := range entityRegistry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func lookupEntity(entityType string) (entityOps, error) {
	ops, ok := entityRegistry[entityType]
	if !ok {
		return entityOps{}, fmt.Errorf("unknown catalog type %q; valid types: %s", entityType, joinList(sortedEntityNames()))
	}
	return ops, nil
}

func joinList(items []string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

// newAPIClient wires config and the keychain-backed session source into a
// client. With requireAuth false, a missing stored session is tolerated and
// requests go out unauthenticated (read endpoints are public).
func newAPIClient(requireAuth bool) (*client.Client, error) {
	cfg, err := config.Load(config.Sources{
		FlagAssetsWebURL: flagAssetsWebURL,
		Getenv:           os.Getenv,
		HomeDir:          os.UserHomeDir,
	})
	if err != nil {
		return nil, err
	}
	apiURL, err := cfg.ServiceURL(os.Getenv, flagAPIURL, "catalog")
	if err != nil {
		return nil, err
	}
	authCfg, err := auth.ResolveConfig(cfg.AuthDomain, cfg.AuthClientID, cfg.AuthAudience)
	if err != nil {
		if !requireAuth {
			// Reads are public; a binary built without baked-in auth settings
			// (e.g. plain `go run`) still serves them, just with no token.
			c, cerr := client.New(apiURL, func(context.Context) (string, error) { return "", nil })
			if cerr != nil {
				return nil, cerr
			}
			withCurlCapture(&c.HTTP)
			return c, nil
		}
		return nil, err
	}
	hc := &http.Client{Timeout: 30 * time.Second}
	source := &auth.SessionSource{Cfg: authCfg, HTTP: hc, Store: auth.KeyringStore{}}
	c, err := client.New(apiURL, tokenFunc(source, requireAuth))
	if err != nil {
		return nil, err
	}
	// Only catalog-api calls are captured; the auth source's own HTTP stays
	// real so a --curl run never prints refresh tokens.
	withCurlCapture(&c.HTTP)
	return c, nil
}

// tokenFunc adapts a session source to the client's TokenSource. Anonymous
// mode downgrades ErrNotLoggedIn to "no Authorization header"; auth-required
// mode turns token failures into the login exit code.
func tokenFunc(source *auth.SessionSource, requireAuth bool) client.TokenSource {
	return func(ctx context.Context) (string, error) {
		token, err := source.Token(ctx)
		if err != nil {
			switch {
			case !requireAuth && errors.Is(err, auth.ErrNotLoggedIn):
				return "", nil
			case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			default:
				err = authExit(err)
			}
		}
		return token, err
	}
}

// changedValues gathers provided flags as string slices; works for both
// single and repeatable flags registered by the entity table.
func changedValues(cmd *cobra.Command) map[string][]string {
	values := map[string][]string{}
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if arr, err := cmd.Flags().GetStringArray(f.Name); err == nil {
			values[f.Name] = arr
			return
		}
		s, _ := cmd.Flags().GetString(f.Name)
		values[f.Name] = []string{s}
	})
	return values
}

func newAddCommand() *cobra.Command {
	add := &cobra.Command{
		Use:   "add",
		Short: "Create a new catalog record",
	}
	for _, name := range sortedEntityNames() {
		ops := entityRegistry[name]
		child := &cobra.Command{
			Use:   fmt.Sprintf("%s <name> [flags]", name),
			Short: fmt.Sprintf("Create a new %s", name),
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := buildAPIClient()
				if err != nil {
					return err
				}
				values := map[string][]string{ops.primaryFlag: {args[0]}}
				for k, v := range changedValues(cmd) {
					if k == ops.primaryFlag && len(v) == 1 {
						continue // positional already carries it
					}
					values[k] = v
				}
				rec, err := ops.buildCreate(values)
				if err != nil {
					return err
				}
				id, err := ops.create(cmd.Context(), c, rec)
				if err != nil {
					return writeErr(err)
				}
				cmd.Printf("Created %s %s\n", name, *id)
				return nil
			},
		}
		ops.register(child.Flags())
		add.AddCommand(child)
		addChildren[name] = child
	}
	return add
}

func newEditCommand() *cobra.Command {
	edit := &cobra.Command{
		Use:   "edit",
		Short: "Update an existing catalog record",
	}
	for _, name := range sortedEntityNames() {
		ops := entityRegistry[name]
		af, hasAsset := lookupAssetFlag(name)
		child := &cobra.Command{
			Use:   fmt.Sprintf("%s <name-or-id> [flags]", name),
			Short: fmt.Sprintf("Edit a %s", name),
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				fields, err := ops.buildPatch(changedValues(cmd))
				if err != nil {
					return err
				}
				assetPath := ""
				if hasAsset {
					assetPath, _ = cmd.Flags().GetString(af.name)
				}
				if len(fields) == 0 && assetPath == "" {
					return usageErr("no properties to update; pass at least one property flag")
				}
				c, err := buildAPIClient()
				if err != nil {
					return err
				}
				id, err := resolveRef(cmd.Context(), c, ops, args[0])
				if err != nil {
					return err
				}
				if assetPath != "" {
					disp, err := af.apply(cmd.Context(), c, id, assetPath)
					if err != nil {
						return writeErr(err)
					}
					cmd.Println(dispositionText(disp))
				}
				if len(fields) == 0 {
					return nil
				}
				_, disp, err := ops.patch(cmd.Context(), c, id, fields)
				if err != nil {
					return writeErr(err)
				}
				cmd.Println(dispositionText(disp))
				return nil
			},
		}
		ops.register(child.Flags())
		if hasAsset {
			child.Flags().String(af.name, "", af.usage)
		}
		edit.AddCommand(child)
		editChildren[name] = child
	}
	return edit
}

func newViewCommand() *cobra.Command {
	view := &cobra.Command{
		Use:   "view",
		Short: "Show one catalog record",
	}
	for _, name := range sortedEntityNames() {
		ops := entityRegistry[name]
		child := &cobra.Command{
			Use:   fmt.Sprintf("%s <name-or-id>", name),
			Short: fmt.Sprintf("View a %s", name),
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				format, err := formatFromFlags(cmd)
				if err != nil {
					return err
				}
				c, err := buildAnonClient()
				if err != nil {
					return err
				}
				id, err := resolveRef(cmd.Context(), c, ops, args[0])
				if err != nil {
					return err
				}
				rec, err := ops.get(cmd.Context(), c, id)
				if err != nil {
					return writeErr(err)
				}
				return printRecord(cmd, rec, format)
			},
		}
		child.Flags().Bool("json", false, "emit raw JSON")
		child.Flags().Bool("yaml", false, "emit YAML")
		view.AddCommand(child)
		viewChildren[name] = child
	}
	return view
}

func newDeleteCommand() *cobra.Command {
	del := &cobra.Command{
		Use:   "delete",
		Short: "Delete a catalog record",
	}
	for _, name := range sortedEntityNames() {
		ops := entityRegistry[name]
		child := &cobra.Command{
			Use:   fmt.Sprintf("%s <name-or-id>", name),
			Short: fmt.Sprintf("Delete a %s", name),
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := buildAPIClient()
				if err != nil {
					return err
				}
				id, err := resolveRef(cmd.Context(), c, ops, args[0])
				if err != nil {
					return err
				}
				if err := confirmDelete(cmd, name, id); err != nil {
					if errors.Is(err, ErrDeclined) {
						cmd.Println("Delete cancelled; record unchanged.")
						return nil
					}
					return err
				}
				if err := ops.del(cmd.Context(), c, id); err != nil {
					return writeErr(err)
				}
				cmd.Printf("Deleted %s %s\n", name, id)
				return nil
			},
		}
		del.AddCommand(child)
		deleteChildren[name] = child
	}
	return del
}

// dispositionText describes what the server did with a write.
func dispositionText(disp *client.WriteDisposition) string {
	if disp == nil {
		return ""
	}
	if disp.Submitted {
		if disp.Message != "" {
			return fmt.Sprintf("Submitted proposed change: %s", disp.Message)
		}
		return "Submitted proposed change for review"
	}
	return fmt.Sprintf("Updated live record (version %d)", disp.Version)
}

// writeErr maps client failures onto CLI exit conventions; auth errors exit 3.
func writeErr(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if client.IsAuthError(err) || auth.IsAuthRequired(err) {
		return authExit(err)
	}
	return err
}
