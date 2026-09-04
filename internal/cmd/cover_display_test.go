package cmd

import (
	"net/http"
	"strings"
	"testing"
)

const coverURLVolumeID = "bbbbbbbbbbbbbbbbbbbbbbbb"

const volumeWithCoverJSON = `{"data":{"type":"volume","id":"` + coverURLVolumeID +
	`","attributes":{"title":"Dungeon World","coverAssetId":"` + coverURLVolumeID + `"}}}`

func TestViewVolumePrintsCoverURLWhenAssetsWebConfigured(t *testing.T) {
	newCmdFixture(t, http.StatusOK, volumeWithCoverJSON)
	oldAssetsURL := flagAssetsWebURL
	flagAssetsWebURL = "https://assets-web.example.com"
	t.Cleanup(func() { flagAssetsWebURL = oldAssetsURL })

	child := viewChildren["volume"]
	out := runEntityCommand(t, child, []string{coverURLVolumeID})

	want := "coverURL:      https://assets-web.example.com/asset/cover/" + coverURLVolumeID
	if !strings.Contains(out, want) {
		t.Errorf("output missing cover URL:\n%s\nwant line: %s", out, want)
	}
}

func TestViewVolumeOmitsCoverURLWhenAssetsWebNotConfigured(t *testing.T) {
	newCmdFixture(t, http.StatusOK, volumeWithCoverJSON)
	// displayAssetsWebURL reads the real config file via os.UserHomeDir;
	// sandbox HOME so a developer's actual ~/.config/sweetrpg/cli.yaml
	// can't mask the "unconfigured" case under test.
	t.Setenv("HOME", t.TempDir())
	oldAssetsURL := flagAssetsWebURL
	flagAssetsWebURL = ""
	t.Cleanup(func() { flagAssetsWebURL = oldAssetsURL })

	child := viewChildren["volume"]
	out := runEntityCommand(t, child, []string{coverURLVolumeID})

	if strings.Contains(out, "coverURL:") {
		t.Errorf("output should omit coverURL when assets-web isn't configured:\n%s", out)
	}
}

func TestViewVolumeJSONOutputStaysRawWithoutCoverURL(t *testing.T) {
	newCmdFixture(t, http.StatusOK, volumeWithCoverJSON)
	flagAssetsWebURL = "https://assets-web.example.com"
	t.Cleanup(func() { flagAssetsWebURL = "" })

	child := viewChildren["volume"]
	if err := child.Flags().Set("json", "true"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Flags().Set("json", "false") })
	out := runEntityCommand(t, child, []string{coverURLVolumeID})

	if strings.Contains(out, "coverURL") {
		t.Errorf("--json output must stay the raw API representation, got:\n%s", out)
	}
}
