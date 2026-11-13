package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestVersionCommandPrintsBuildInfo(t *testing.T) {
	buildTree()
	cmd := rootCmd
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{"version"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("version command failed: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(out.String()), "sweetrpg ") {
		t.Errorf("version output = %q, want it to start with \"sweetrpg \"", out.String())
	}
}

func TestBuildVersionNeverEmpty(t *testing.T) {
	if v := buildVersion(); v == "" {
		t.Error("buildVersion() returned an empty string")
	}
}
