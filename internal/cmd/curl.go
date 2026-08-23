package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
)

// errCurlEmitted aborts a command after its request was rendered as cURL;
// isCurlExit turns it into a clean exit 0.
var errCurlEmitted = errors.New("cURL command printed")

// curlOut receives rendered commands; a var so tests can capture output.
var curlOut io.Writer = os.Stdout

// flagCurl mirrors the global --curl flag.
var flagCurl bool

// curlTransport renders each outgoing request as an equivalent cURL command
// and refuses to send it. Flows that need server data to continue (name
// resolution feeding later requests) stop after their first request; pass
// IDs instead of names to see write requests directly.
type curlTransport struct{}

func (curlTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// The flow dies here, so silencing cobra's error/usage printing is safe:
	// only the rendered command should reach the terminal. Errors that happen
	// before any request (config, auth) still print normally.
	rootCmd.SilenceErrors = true
	rootCmd.SilenceUsage = true
	_, _ = fmt.Fprintln(curlOut, renderCurl(req))
	return nil, errCurlEmitted
}

// withCurlCapture swaps in the capture transport when --curl is set.
func withCurlCapture(c **http.Client) {
	if flagCurl && c != nil && *c != nil {
		*c = &http.Client{Transport: curlTransport{}}
	}
}

// isCurlExit reports whether an error is the capture transport's abort,
// surviving any wrapping on the way up.
func isCurlExit(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, errCurlEmitted) || strings.Contains(err.Error(), errCurlEmitted.Error())
}

// renderCurl builds a runnable-except-for-the-token cURL invocation: headers
// print as set, Authorization values are masked, JSON bodies are pretty-
// printed, and everything is single-quote shell-escaped.
func renderCurl(req *http.Request) string {
	head := "curl"
	if req.Method != http.MethodGet {
		head += " -X " + req.Method
	}
	parts := []string{head + " " + shellQuote(req.URL.String())}

	keys := make([]string, 0, len(req.Header))
	for k := range req.Header {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := req.Header.Get(k)
		if strings.EqualFold(k, "Authorization") {
			v = maskAuth(v)
		}
		parts = append(parts, "-H "+shellQuote(k+": "+v))
	}
	if body := peekBody(req); len(body) > 0 {
		parts = append(parts, "--data "+shellQuote(string(body)))
	}
	return strings.Join(parts, " \\\n  ")
}

// peekBody reads the request body for rendering without consuming it for real:
// GetBody replays it when available; otherwise the raw bytes are restored onto
// the request. Parseable JSON is indented for readability.
func peekBody(req *http.Request) []byte {
	if req.Body == nil || req.Body == http.NoBody {
		return nil
	}
	var raw []byte
	if req.GetBody != nil {
		br, err := req.GetBody()
		if err != nil {
			return nil
		}
		defer func() { _ = br.Close() }()
		raw, _ = io.ReadAll(br)
	} else {
		raw, _ = io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(raw))
	}
	if len(raw) == 0 {
		return nil
	}
	var buf bytes.Buffer
	if json.Indent(&buf, raw, "", "  ") == nil && json.Valid(buf.Bytes()) {
		return buf.Bytes()
	}
	return raw
}

// maskAuth keeps tokens out of terminals and scrollback: only the scheme
// survives ("Bearer <redacted>").
func maskAuth(v string) string {
	parts := strings.SplitN(v, " ", 2)
	if len(parts) == 2 {
		return parts[0] + " <redacted>"
	}
	return "<redacted>"
}

// shellQuote wraps s in single quotes, escaping embedded quotes so the
// printed command round-trips through POSIX shells.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
