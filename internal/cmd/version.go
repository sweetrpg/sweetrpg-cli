package cmd

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// newVersionCommand reports what's actually running. It reads Go's build-info
// mechanism (the same data `go version -m` reads) rather than requiring
// ldflags wired into the release workflow - a stale `go install @latest`
// pinned to a pre-fix pseudo-version shows up here too, which is the gap that
// prompted adding this command in the first place.
func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the sweetrpg CLI version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.Printf("sweetrpg %s\n", buildVersion())
			return nil
		},
	}
}

// buildVersion formats the module version plus a short VCS revision, e.g.
// "v0.2.0 (1a8f7a05d4c7)" or "v0.2.0 (1a8f7a05d4c7, dirty)". Falls back to
// "(devel)" for a plain `go build` with no embedded module version (e.g.
// built from within its own repo rather than `go install`). Used both by the
// version subcommand (prefixed with the tool name) and rootCmd.Version
// (which cobra's own --version output already prefixes).
func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown (no build info embedded)"
	}

	version := info.Main.Version
	if version == "" {
		version = "(devel)"
	}

	var revision string
	dirty := false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			dirty = setting.Value == "true"
		}
	}

	out := version
	if revision != "" {
		if len(revision) > 12 {
			revision = revision[:12]
		}
		if dirty {
			out += fmt.Sprintf(" (%s, dirty)", revision)
		} else {
			out += fmt.Sprintf(" (%s)", revision)
		}
	}
	return out
}
