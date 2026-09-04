package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"

	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
	"github.com/sweetrpg/sweetrpg-cli/internal/client"
)

var (
	flagYes   bool
	flagForce bool

	// stdoutIsTTY and pickCandidate are vars so tests can drive both branches.
	stdoutIsTTY   = defaultIsTTY
	pickCandidate = interactivePick
)

var objectIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{24}$`)

func defaultIsTTY() bool {
	info, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// resolveRef maps a <name-or-id> argument to a record ID: ID-shaped strings
// short-circuit; names resolve via search, prompting only when ambiguous.
func resolveRef(ctx context.Context, c *client.Client, ops entityOps, ref string) (string, error) {
	if objectIDPattern.MatchString(ref) {
		return ref, nil
	}
	candidates, err := ops.find(ctx, c, ref)
	if err != nil {
		return "", err
	}
	switch len(candidates) {
	case 0:
		return "", fmt.Errorf("no %s record found for %q", ops.spec.Name, ref)
	case 1:
		return candidates[0].ID, nil
	}
	if flagYes || !stdoutIsTTY() {
		lines := make([]string, 0, len(candidates))
		for _, cand := range candidates {
			lines = append(lines, fmt.Sprintf("%s (%s) %s", cand.ID, cand.Label, cand.Detail))
		}
		return "", fmt.Errorf("%q is ambiguous - %d %s records match; pass an ID instead:\n%s",
			ref, len(candidates), ops.spec.Name, joinList(lines))
	}
	idx, err := pickCandidate(candidates)
	if err != nil {
		return "", fmt.Errorf("selection cancelled: %w", err)
	}
	return candidates[idx].ID, nil
}

// interactivePick shows a promptui selector over ambiguous candidates.
func interactivePick(candidates []candidate) (int, error) {
	items := make([]string, len(candidates))
	for i, cand := range candidates {
		detail := cand.Detail
		if detail != "" {
			detail = " " + detail
		}
		items[i] = fmt.Sprintf("%s%s [%s]", cand.Label, detail, cand.ID)
	}
	prompt := promptui.Select{
		Label: "Multiple matches - choose one",
		Items: items,
		Size:  10,
		Templates: &promptui.SelectTemplates{
			Label:    "{{ . }}:",
			Active:   fmt.Sprintf("%s {{ . | cyan }}", promptui.IconSelect),
			Inactive: "  {{ . }}",
			Selected: fmt.Sprintf("%s {{ . }}", promptui.IconGood),
		},
	}
	idx, _, err := prompt.Run()
	return idx, err
}

// confirmPrompt asks the user interactively; a var so tests can override.
var confirmPrompt = interactiveConfirm

func interactiveConfirm(label string) bool {
	prompt := promptui.Prompt{
		Label:     label,
		IsConfirm: true,
		Default:   "n",
	}
	result, err := prompt.Run()
	if err != nil {
		return false
	}
	return result == "y" || result == "Y" || result == "yes"
}

// ErrDeclined marks a user-declined confirmation: a normal outcome that must
// exit 0 without touching the API.
var ErrDeclined = errors.New("declined")

// confirmDelete enforces explicit confirmation before destructive deletes.
func confirmDelete(cmd *cobra.Command, entity, id string) error {
	if flagForce {
		return nil
	}
	if flagYes || !stdoutIsTTY() {
		return fmt.Errorf("refusing to delete %s %s in non-interactive mode without --force", entity, id)
	}
	if !confirmPrompt(fmt.Sprintf("Delete %s %s? This cannot be undone", entity, id)) {
		return ErrDeclined
	}
	return nil
}
