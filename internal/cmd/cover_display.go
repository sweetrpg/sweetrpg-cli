package cmd

import (
	"os"
	"strings"

	"github.com/spf13/cobra"
	vo "github.com/sweetrpg/catalog-objects.go/vo"
	"github.com/sweetrpg/sweetrpg-cli/internal/config"
)

// printCoverURL prints the volume's cover as a viewable assets-web URL
// (assets-web serves GET /asset/cover/<id>) below the record's own fields,
// for human-readable `view volume` output. JSON/YAML output stays the raw
// API representation per spec, so this never touches those. Best-effort: if
// assets-web isn't configured, it prints nothing rather than failing the
// view over a display nicety.
func printCoverURL(cmd *cobra.Command, name string, rec any) {
	if name != "volume" {
		return
	}
	v, ok := rec.(*vo.VolumeVO)
	if !ok || v.CoverAssetId == "" {
		return
	}
	base := displayAssetsWebURL()
	if base == "" {
		return
	}
	cmd.Printf("coverURL:      %s\n", strings.TrimRight(base, "/")+"/asset/cover/"+v.CoverAssetId)
}

func displayAssetsWebURL() string {
	cfg, err := config.Load(config.Sources{
		FlagAssetsWebURL: flagAssetsWebURL,
		Getenv:           os.Getenv,
		HomeDir:          os.UserHomeDir,
	})
	if err != nil {
		return ""
	}
	return cfg.AssetsWebURL
}
