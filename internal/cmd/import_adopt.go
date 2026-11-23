package cmd

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	vo "github.com/sweetrpg/catalog-objects.go/vo"
	modelcore "github.com/sweetrpg/model-core.go/vo"
	"github.com/sweetrpg/sweetrpg-cli/internal/client"
	"github.com/sweetrpg/sweetrpg-cli/internal/dtrpg"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// maxSearchPages caps how many catalog-api pages one candidate search reads. The search
// token is the title's longest word, so a page of scanPageSize results is already a
// superset of the real candidates for any normal title.
// ponytail: a token shared by more than maxSearchPages*scanPageSize volumes could hide a
// candidate; add a title-equality filter to catalog-api if that ever happens.
const maxSearchPages = 2

// normalizeName folds a title or publisher name to the match key: Unicode NFKC, case-folded,
// with punctuation and whitespace runs collapsed to single spaces.
func normalizeName(s string) string {
	s = cases.Fold().String(norm.NFKC.String(s))
	words := strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	return strings.Join(words, " ")
}

// searchToken picks the longest word of a normalized name as the catalog-api search term.
// catalog-api's filter[q] is a case-insensitive substring match, so one word of the title
// finds every volume whose title differs only in case, punctuation or spacing.
func searchToken(normalized string) string {
	best := ""
	for _, w := range strings.Fields(normalized) {
		if len([]rune(w)) > len([]rune(best)) {
			best = w
		}
	}
	return best
}

// volumeSearcher finds volumes whose title, description or tags contain q.
type volumeSearcher interface {
	SearchVolumes(ctx context.Context, q string) ([]*vo.VolumeVO, error)
}

// volumePatcher applies a volume update through catalog-api's normal PATCH path.
type volumePatcher interface {
	PatchVolume(ctx context.Context, id string, fields map[string]any) (client.WriteDisposition, error)
}

// publisherLookup returns the existing publisher ID for a name without creating one.
type publisherLookup func(ctx context.Context, name string) (string, bool, error)

// apiVolumes is the catalog-api backed volumeSearcher and volumePatcher.
type apiVolumes struct{ c *client.Client }

func (a apiVolumes) SearchVolumes(ctx context.Context, q string) ([]*vo.VolumeVO, error) {
	var out []*vo.VolumeVO
	for page := 0; page < maxSearchPages; page++ {
		got, err := client.List[vo.VolumeVO](ctx, a.c, "volumes", client.ListOptions{
			Filters: []client.Filter{{Field: "q", Values: []string{q}}},
			Start:   page * scanPageSize,
			Limit:   scanPageSize,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, got...)
		if len(got) < scanPageSize {
			break
		}
	}
	return out, nil
}

func (a apiVolumes) PatchVolume(ctx context.Context, id string, fields map[string]any) (client.WriteDisposition, error) {
	_, disp, err := client.Patch[vo.VolumeVO](ctx, a.c, "volumes", id, fields)
	return disp, err
}

// adoption pairs a DTRPG product with the one legacy volume it will claim.
type adoption struct {
	prod   dtrpg.Product
	volume *vo.VolumeVO
}

// ambiguity is a product that matched several legacy volumes, or competes with another
// product for the same one; nothing is written for it.
type ambiguity struct {
	prod      dtrpg.Product
	volumeIDs []string
}

// matchPlan splits the products that have no volume with their ID into create / adopt / skip.
type matchPlan struct {
	toCreate  []dtrpg.Product
	toAdopt   []adoption
	ambiguous []ambiguity
}

// planMatches looks for exactly one unclaimed volume with the product's normalized title and
// publisher. Zero matches create, one adopts, several are reported and skipped. A volume
// wanted by two products in the same run is ambiguous for both.
func planMatches(ctx context.Context, s volumeSearcher, pub publisherLookup, products []dtrpg.Product) (matchPlan, error) {
	var plan matchPlan
	matches := make([][]*vo.VolumeVO, len(products))
	wanted := map[string]int{}
	for i, prod := range products {
		found, err := candidateVolumes(ctx, s, pub, prod)
		if err != nil {
			return matchPlan{}, fmt.Errorf("searching catalog for %q: %w", prod.Title, err)
		}
		matches[i] = found
		if len(found) == 1 {
			wanted[found[0].ID]++
		}
	}
	for i, prod := range products {
		switch found := matches[i]; {
		case len(found) == 0:
			plan.toCreate = append(plan.toCreate, prod)
		case len(found) == 1 && wanted[found[0].ID] == 1:
			plan.toAdopt = append(plan.toAdopt, adoption{prod: prod, volume: found[0]})
		default:
			plan.ambiguous = append(plan.ambiguous, ambiguity{prod: prod, volumeIDs: volumeIDs(found)})
		}
	}
	return plan, nil
}

func volumeIDs(vols []*vo.VolumeVO) []string {
	ids := make([]string, len(vols))
	for i, v := range vols {
		ids[i] = v.ID
	}
	return ids
}

// candidateVolumes returns the volumes that share the product's normalized title and
// publisher and carry no DTRPG product ID.
func candidateVolumes(ctx context.Context, s volumeSearcher, pub publisherLookup, prod dtrpg.Product) ([]*vo.VolumeVO, error) {
	title := normalizeName(prod.Volume.Title)
	token := searchToken(title)
	if token == "" {
		return nil, nil
	}
	var publisherID string
	if prod.PublisherName != "" {
		id, ok, err := pub(ctx, prod.PublisherName)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, nil // no such publisher yet, so no volume can carry it
		}
		publisherID = id
	}
	found, err := s.SearchVolumes(ctx, token)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []*vo.VolumeVO
	for _, v := range found {
		if seen[v.ID] || normalizeName(v.Title) != title || productIDOf(v) != "" || !hasPublisher(v, publisherID) {
			continue
		}
		seen[v.ID] = true
		out = append(out, v)
	}
	return out, nil
}

// hasPublisher reports whether v is linked to publisherID, or to no publisher when the
// product names none (publisherID empty).
func hasPublisher(v *vo.VolumeVO, publisherID string) bool {
	if publisherID == "" {
		return len(v.Publishers) == 0
	}
	for _, p := range v.Publishers {
		if p != nil && p.ID == publisherID {
			return true
		}
	}
	return false
}

func productIDOf(v *vo.VolumeVO) string {
	for _, p := range v.Properties {
		if p.Name == dtrpg.PropProductID && p.Value != "" {
			return p.Value
		}
	}
	return ""
}

// adoptionFields is the PATCH body that claims a legacy volume: its existing properties plus
// the product ID (PATCH replaces the whole properties list, so the others are carried over).
// Title, description, tags and the cover are not part of it.
func adoptionFields(v *vo.VolumeVO, prod dtrpg.Product) (map[string]any, error) {
	var productID string
	for _, p := range prod.Volume.Properties {
		if p.Name == dtrpg.PropProductID {
			productID = p.Value
		}
	}
	if productID == "" {
		return nil, fmt.Errorf("product %q has no product ID", prod.Title)
	}
	props := make([]modelcore.PropertyVO, 0, len(v.Properties)+1)
	for _, p := range v.Properties {
		if p.Name != dtrpg.PropProductID {
			props = append(props, p)
		}
	}
	props = append(props, modelcore.PropertyVO{Name: dtrpg.PropProductID, Value: productID})
	return map[string]any{"properties": props}, nil
}

// adoptVolume records the product ID on the legacy volume through the normal update path.
// catalog-api only lets editors and admins change properties; a submitter token gets a 400,
// which surfaces here as the returned error.
func adoptVolume(ctx context.Context, p volumePatcher, a adoption) error {
	fields, err := adoptionFields(a.volume, a.prod)
	if err != nil {
		return err
	}
	disp, err := p.PatchVolume(ctx, a.volume.ID, fields)
	if err != nil {
		return err
	}
	if disp.Submitted {
		return fmt.Errorf("catalog-api queued the change for review (version %d) instead of applying it", disp.Version)
	}
	return nil
}
