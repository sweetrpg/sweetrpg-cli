package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/sweetrpg/sweetrpg-cli/internal/auth"
	"github.com/sweetrpg/sweetrpg-cli/internal/config"
)

var (
	flagAPIService  string
	flagAPIFields   []string
	flagAPIRawField []string
	flagAPIHeaders  []string

	// resolveAPIRequest resolves the target service's base URL and a token
	// source for it. A var so tests can point it at a fixture server and a
	// fake token without touching real config or the OS keychain.
	resolveAPIRequest = defaultResolveAPIRequest
)

// defaultResolveAPIRequest loads config/auth the same way every other
// command does; --api-url only applies when the caller targeted "catalog"
// with it, since that flag is the catalog command tree's own override.
func defaultResolveAPIRequest(service string) (baseURL string, tokens func(context.Context) (string, error), err error) {
	cfg, err := config.Load(config.Sources{
		FlagAssetsWebURL: flagAssetsWebURL,
		Getenv:           os.Getenv,
		HomeDir:          os.UserHomeDir,
	})
	if err != nil {
		return "", nil, err
	}
	var apiURLOverride string
	if service == "catalog" {
		apiURLOverride = flagAPIURL
	}
	baseURL, err = cfg.ServiceURL(os.Getenv, apiURLOverride, service)
	if err != nil {
		return "", nil, err
	}
	authCfg, err := auth.DefaultConfig()
	if err != nil {
		return "", nil, err
	}
	source := &auth.SessionSource{Cfg: authCfg, HTTP: &http.Client{Timeout: 30 * time.Second}, Store: auth.KeyringStore{}}
	return baseURL, source.Token, nil
}

// newAPICommand builds the generic authenticated passthrough command, in the
// spirit of `gh api`: it reuses the same session token attachment and
// --curl preview transport every other command uses, against any configured
// service rather than only catalog-api.
func newAPICommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "api [method] <path>",
		Short: "Send an authenticated request against any platform service",
		Long: "Send an arbitrary authenticated HTTP request against a named platform service,\n" +
			"in the spirit of `gh api`.\n\n" +
			"  sweetrpg api GET /volumes/123 --service catalog\n" +
			"  sweetrpg api POST /publishers --service catalog --field name=\"Evil Hat\"\n\n" +
			"The method defaults to GET with no body, POST when --field/--raw-field is set.",
		Args: cobra.RangeArgs(1, 2),
		RunE: runAPI,
	}
	c.Flags().StringVar(&flagAPIService, "service", "", "target service name (catalog, game-room, users, admin, auth)")
	c.Flags().StringArrayVar(&flagAPIFields, "field", nil, "JSON-typed request body field key=value (repeatable)")
	c.Flags().StringArrayVar(&flagAPIRawField, "raw-field", nil, "string request body field key=value (repeatable)")
	c.Flags().StringArrayVarP(&flagAPIHeaders, "header", "H", nil, "extra request header \"Key: Value\" (repeatable)")
	return c
}

func runAPI(cmd *cobra.Command, args []string) error {
	if strings.TrimSpace(flagAPIService) == "" {
		return usageErr("--service is required")
	}

	method, path := parseAPIMethodPath(args)
	body, contentType, err := buildAPIBody(flagAPIFields, flagAPIRawField)
	if err != nil {
		return usageErr("%v", err)
	}
	if method == "" {
		if body != nil {
			method = http.MethodPost
		} else {
			method = http.MethodGet
		}
	}

	headers, err := parseAPIHeaders(flagAPIHeaders)
	if err != nil {
		return usageErr("%v", err)
	}

	baseURL, tokens, err := resolveAPIRequest(flagAPIService)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(strings.ToUpper(method), joinBaseAndPath(baseURL, path), body)
	if err != nil {
		return usageErr("building request: %v", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	token, err := tokens(cmd.Context())
	if err != nil {
		// A --curl preview still renders without a session (matching an
		// anonymous read against a public endpoint); every other run needs
		// a real token.
		if !flagCurl || !errors.Is(err, auth.ErrNotLoggedIn) {
			return authExit(err)
		}
		token = ""
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	withCurlCapture(&client)
	resp, err := client.Do(req)
	if err != nil {
		if isCurlExit(err) {
			return nil
		}
		return err
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return fmt.Errorf("reading response body: %w", err)
	}
	cmd.Println(string(raw))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &ExitError{Code: 1, Err: fmt.Errorf("%s returned %d", flagAPIService, resp.StatusCode)}
	}
	return nil
}

// parseAPIMethodPath splits positional args into method (possibly empty, to
// be defaulted by body presence) and path.
func parseAPIMethodPath(args []string) (method, path string) {
	if len(args) == 2 {
		return args[0], args[1]
	}
	return "", args[0]
}

// joinBaseAndPath appends path (with or without a leading slash) to base,
// trimming any duplicate slash at the seam.
func joinBaseAndPath(base, path string) string {
	base = strings.TrimRight(base, "/")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return base + path
}

// buildAPIBody constructs a JSON request body from --field (type-sniffed:
// true/false/numeric encode as their JSON type, everything else as a
// string) and --raw-field (always a string) flags. Returns a nil body when
// neither is set.
func buildAPIBody(fields, rawFields []string) (io.Reader, string, error) {
	if len(fields) == 0 && len(rawFields) == 0 {
		return nil, "", nil
	}
	payload := map[string]any{}
	for _, kv := range fields {
		k, v, err := splitKeyValue(kv, "--field")
		if err != nil {
			return nil, "", err
		}
		payload[k] = sniffJSONValue(v)
	}
	for _, kv := range rawFields {
		k, v, err := splitKeyValue(kv, "--raw-field")
		if err != nil {
			return nil, "", err
		}
		payload[k] = v
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, "", fmt.Errorf("encoding request body: %w", err)
	}
	return bytes.NewReader(encoded), "application/json", nil
}

func splitKeyValue(kv, flagName string) (key, value string, err error) {
	i := strings.IndexByte(kv, '=')
	if i < 0 {
		return "", "", fmt.Errorf("%s value %q must be key=value", flagName, kv)
	}
	return kv[:i], kv[i+1:], nil
}

// sniffJSONValue type-sniffs a --field value the way `gh api -f` does:
// true/false become booleans, a valid number becomes a JSON number,
// everything else stays a string.
func sniffJSONValue(v string) any {
	switch v {
	case "true":
		return true
	case "false":
		return false
	}
	if n, err := strconv.ParseFloat(v, 64); err == nil {
		return n
	}
	return v
}

// parseAPIHeaders parses repeatable "Key: Value" flag values.
func parseAPIHeaders(raw []string) (map[string]string, error) {
	headers := map[string]string{}
	for _, h := range raw {
		i := strings.IndexByte(h, ':')
		if i < 0 {
			return nil, fmt.Errorf("-H value %q must be \"Key: Value\"", h)
		}
		headers[strings.TrimSpace(h[:i])] = strings.TrimSpace(h[i+1:])
	}
	return headers, nil
}
