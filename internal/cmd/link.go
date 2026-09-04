package cmd

import (
	"context"
	"fmt"
	"slices"

	"github.com/spf13/cobra"
	"github.com/sweetrpg/sweetrpg-cli/internal/client"
	vo "github.com/sweetrpg/catalog-objects.go/vo"
)

// linkRule is one supported pairing, stored volume-first. Direct rules patch a
// full-replace ID array on the volume (Field); person links go through the
// credits diff, which the server reconciles into contribution records.
// volume-license is deliberately absent: catalog-objects models the relation,
// but catalog-api's patch request has no licenseIds field, so there is no
// write surface for it.
type linkRule struct {
	a, b  string
	field string // volume PATCH wire key: "publisherIds" | "studioIds" | "systemIds" | "credits"
}

var linkMatrix = []linkRule{
	{a: "volume", b: "publisher", field: "publisherIds"},
	{a: "volume", b: "studio", field: "studioIds"},
	{a: "volume", b: "system", field: "systemIds"},
	{a: "volume", b: "person", field: "credits"},
}

func findLinkRule(t1, t2 string) (linkRule, bool) {
	for _, r := range linkMatrix {
		if (r.a == t1 && r.b == t2) || (r.a == t2 && r.b == t1) {
			return r, true
		}
	}
	return linkRule{}, false
}

// counterpartList names every type t may link to, for invalid-pairing errors.
func counterpartList(t string) []string {
	out := []string{}
	for _, r := range linkMatrix {
		if r.a == t {
			out = append(out, r.b)
		} else if r.b == t {
			out = append(out, r.a)
		}
	}
	return out
}

// creditPair mirrors catalog-api's creditRequest: one desired
// (person, contribution type) entry of the volume's full-replace credits list.
type creditPair struct {
	PersonID string `json:"personId"`
	Role     string `json:"contributionType"`
}

// flagRole holds --role for the running command; only one command executes
// per process, so a shared var matches the other flag globals here.
var flagRole string

func newLinkCommand() *cobra.Command {
	link := &cobra.Command{
		Use:   "link <type1> <name-or-id1> <type2> <name-or-id2>",
		Short: "Connect two catalog entities",
		Long: "Connect two catalog entities, either argument order accepted, e.g.\n" +
			"  sweetrpg catalog link volume \"Dungeon World\" publisher \"Evil Hat Productions\"\n\n" +
			"Supported pairs: volume-publisher, volume-studio, volume-system, volume-person.\n" +
			"Person links take --role and are stored as contribution records.",
		Args: cobra.ExactArgs(4),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPair(cmd, args, true)
		},
	}
	link.Flags().StringVar(&flagRole, "role", "author", "contribution role for person links (e.g. author, artist)")
	return link
}

func newUnlinkCommand() *cobra.Command {
	unlink := &cobra.Command{
		Use:   "unlink <type1> <name-or-id1> <type2> <name-or-id2>",
		Short: "Disconnect two linked catalog entities",
		Long: "Disconnect two linked catalog entities, either argument order accepted.\n" +
			"Unlinking a person drops every role they hold on the volume.",
		Args: cobra.ExactArgs(4),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPair(cmd, args, false)
		},
	}
	unlink.Flags().StringVar(&flagRole, "role", "author", "contribution role for person links (e.g. author, artist)")
	return unlink
}

// runPair validates the pairing, resolves both refs, and applies or removes
// the link. Work always happens on the volume side of the rule.
func runPair(cmd *cobra.Command, args []string, add bool) error {
	t1, ref1, t2, ref2 := args[0], args[1], args[2], args[3]
	opsA, ok := entityRegistry[t1]
	if !ok {
		return usageErr("unknown entity type %q; valid types: %s", t1, joinList(sortedEntityNames()))
	}
	opsB, ok := entityRegistry[t2]
	if !ok {
		return usageErr("unknown entity type %q; valid types: %s", t2, joinList(sortedEntityNames()))
	}
	rule, ok := findLinkRule(t1, t2)
	if !ok {
		return usageErr("links between %s and %s aren't supported; valid counterparts for %s: %s",
			t1, t2, t1, joinList(counterpartList(t1)))
	}

	c, err := buildAPIClient()
	if err != nil {
		return err
	}
	ctx := cmd.Context()

	idForType := map[string]string{}
	for _, side := range []struct {
		ops entityOps
		ref string
	}{{opsA, ref1}, {opsB, ref2}} {
		id, err := resolveRef(ctx, c, side.ops, side.ref)
		if err != nil {
			return err
		}
		idForType[side.ops.spec.Name] = id
	}
	volOps := entityRegistry[rule.a]
	volID, otherID := idForType[rule.a], idForType[rule.b]

	fields, verb, notice, err := planLinkChange(ctx, c, volOps, volID, otherID, rule, add)
	if err != nil {
		return err
	}
	if notice != "" {
		cmd.Println(notice)
		return nil
	}
	if _, disp, err := volOps.patch(ctx, c, volID, fields); err != nil {
		return writeErr(err)
	} else if text := dispositionText(disp); text != "" {
		cmd.Println(text)
	}
	prep := map[bool]string{true: "to", false: "from"}[add]
	cmd.Printf("%s %s %s %s volume %s\n", verb, rule.b, otherID, prep, volID)
	return nil
}

// planLinkChange reads current state and returns the full-replace PATCH body,
// the action verb, or a notice when nothing changes (idempotent re-link /
// absent-pair unlink exit zero).
func planLinkChange(
	ctx context.Context, c *client.Client, volOps entityOps,
	volID, otherID string, rule linkRule, add bool,
) (fields map[string]any, verb, notice string, err error) {
	if rule.field == "credits" {
		return planCreditsChange(ctx, c, volID, otherID, add)
	}

	rec, gerr := client.Get[vo.VolumeVO](ctx, c, volOps.spec.Plural, volID)
	if gerr != nil {
		return nil, "", "", writeErr(gerr)
	}
	current := relationIDs(rec, rule.field)
	linked := slices.Contains(current, otherID)

	switch {
	case add && linked:
		return nil, "", fmt.Sprintf("%s %s is already linked to volume %s", rule.b, otherID, volID), nil
	case !add && !linked:
		return nil, "", fmt.Sprintf("%s %s isn't linked to volume %s", rule.b, otherID, volID), nil
	case add:
		current = append(current, otherID)
	default:
		current = slices.DeleteFunc(current, func(id string) bool { return id == otherID })
	}
	verb = map[bool]string{true: "Linked", false: "Unlinked"}[add]
	return map[string]any{rule.field: current}, verb, "", nil
}

// planCreditsChange diffs the volume's single-role contribution pairs against
// the desired set, mirroring applyCreditsDiff on the server: adding appends
// one pair; removing drops every role the person holds on this volume.
func planCreditsChange(
	ctx context.Context, c *client.Client, volID, personID string, add bool,
) (map[string]any, string, string, error) {
	contribs, err := client.List[vo.ContributionVO](ctx, c, "contributions", client.ListOptions{
		Filters: []client.Filter{{Field: "volume_id", Values: []string{volID}}},
	})
	if err != nil {
		return nil, "", "", writeErr(err)
	}
	pairs := make([]creditPair, 0, len(contribs))
	for _, con := range contribs {
		if con.Person == nil || len(con.Roles) != 1 {
			continue // multi-role contributions aren't representable as one pair
		}
		pairs = append(pairs, creditPair{PersonID: con.Person.ID, Role: con.Roles[0]})
	}
	hasRequested := slices.Contains(pairs, creditPair{PersonID: personID, Role: flagRole})
	hasAny := slices.ContainsFunc(pairs, func(p creditPair) bool { return p.PersonID == personID })

	switch {
	case add && hasRequested:
		return nil, "", fmt.Sprintf("person %s already holds the %s credit on volume %s", personID, flagRole, volID), nil
	case !add && !hasAny:
		return nil, "", fmt.Sprintf("person %s isn't linked to volume %s", personID, volID), nil
	case add:
		pairs = append(pairs, creditPair{PersonID: personID, Role: flagRole})
	default:
		pairs = slices.DeleteFunc(pairs, func(p creditPair) bool { return p.PersonID == personID })
	}
	verb := map[bool]string{true: "Linked", false: "Unlinked"}[add]
	return map[string]any{"credits": pairs}, verb, "", nil
}

// relationIDs reads the current ID list off the volume for one patch field.
// Field names compile against VolumeVO's json tags, so schema drift breaks
// the build here too.
func relationIDs(v *vo.VolumeVO, field string) []string {
	switch field {
	case "publisherIds":
		return pointerIDs(v.Publishers)
	case "studioIds":
		return pointerIDs(v.Studios)
	case "systemIds":
		return pointerIDs(v.Systems)
	default:
		return nil
	}
}

// pointerIDs collects IDs from a VO relation slice; nil entries are skipped.
func pointerIDs[T any](srcs []*T) []string {
	out := make([]string, 0, len(srcs))
	for _, s := range srcs {
		if s == nil {
			continue
		}
		out = append(out, voID(s))
	}
	return out
}
