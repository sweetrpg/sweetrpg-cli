package dtrpg

import (
	"strconv"
	"strings"

	"github.com/pilgrimagesoftware/dtrpg-sdk.go/library"
	catvo "github.com/sweetrpg/catalog-objects.go/vo"
	modelcore "github.com/sweetrpg/model-core.go/vo"
)

// Product is one DriveThruRPG ordered product mapped to a catalog volume,
// keeping the fields the importer classifies on alongside the volume payload.
type Product struct {
	ProductID     string // dtrpg_product_id; the idempotency key
	Title         string
	PublisherName string
	Archived      bool
	Volume        catvo.VolumeVO
}

// MapProducts converts a fetched library into catalog volumes. Mapping is
// conservative: only title, short description, and category tags land in volume
// fields; ISBN, purchase date, cover URL, and the raw DTRPG identifiers ride in
// properties where a wrong guess can't pollute a typed field.
func MapProducts(lib Library) []Product {
	publishers, products := indexIncluded(lib.Included)

	out := make([]Product, 0, len(lib.Products))
	for _, item := range lib.Products {
		out = append(out, mapProduct(item, publishers, products))
	}
	return out
}

func mapProduct(
	item library.OrderProductItem,
	publishers map[string]*library.PublisherAttributes,
	products map[string]*library.OrderProductInfo,
) Product {
	attrs := item.Attributes

	info := attrs.Product
	if info == nil {
		info = products[relID(item.Relationships, func(r *library.OrderProductRelationships) *library.RelationshipRef { return r.Product })]
	}

	p := Product{
		ProductID:     strconv.FormatUint(attrs.ProductID, 10),
		Title:         attrs.Name,
		PublisherName: publisherName(attrs, item.Relationships, publishers),
		Archived:      attrs.Archived != 0,
	}

	p.Volume = catvo.VolumeVO{
		Title:       attrs.Name,
		Description: shortDescription(info),
		Tags:        filterTags(attrs.Filters),
		Properties:  properties(attrs, info),
	}
	return p
}

// publisherName prefers the publisher embedded on the product, falling back to
// the sideloaded resource named by the publisher relationship.
func publisherName(
	attrs library.OrderProductAttributes,
	rels *library.OrderProductRelationships,
	publishers map[string]*library.PublisherAttributes,
) string {
	if attrs.Publisher != nil && strings.TrimSpace(attrs.Publisher.Name) != "" {
		return strings.TrimSpace(attrs.Publisher.Name)
	}
	if pub := publishers[relID(rels, func(r *library.OrderProductRelationships) *library.RelationshipRef { return r.Publisher })]; pub != nil {
		return strings.TrimSpace(pub.Name)
	}
	return ""
}

func shortDescription(info *library.OrderProductInfo) string {
	if info == nil || info.Description == nil || info.Description.ShortDescription == nil {
		return ""
	}
	return strings.TrimSpace(*info.Description.ShortDescription)
}

// filterTags turns DriveThruRPG category filters into value-less tags, keeping
// first-seen order and dropping blanks and duplicates.
func filterTags(filters []library.OrderProductFilter) []modelcore.TagVO {
	seen := map[string]bool{}
	out := make([]modelcore.TagVO, 0, len(filters))
	for _, f := range filters {
		name := strings.TrimSpace(f.Name)
		if name == "" || seen[strings.ToLower(name)] {
			continue
		}
		seen[strings.ToLower(name)] = true
		out = append(out, modelcore.TagVO{Name: name})
	}
	return out
}

func properties(attrs library.OrderProductAttributes, info *library.OrderProductInfo) []modelcore.PropertyVO {
	props := make([]modelcore.PropertyVO, 0, 5)
	add := func(name, value string) {
		if strings.TrimSpace(value) != "" {
			props = append(props, modelcore.PropertyVO{Name: name, Kind: "string", Value: value})
		}
	}
	add(PropProductID, strconv.FormatUint(attrs.ProductID, 10))
	add(PropOrderProductID, strconv.FormatUint(attrs.OrderProductID, 10))
	add(PropPurchaseDate, deref(attrs.DatePurchased))
	add(PropISBN, deref(attrs.ISBN))
	add(PropCoverURL, coverURL(info))
	return props
}

// coverURL returns the best available cover image as an absolute URL.
func coverURL(info *library.OrderProductInfo) string {
	if info == nil {
		return ""
	}
	for _, candidate := range []*string{info.WebImage, info.Image, info.Thumbnail, info.Thumbnail100} {
		if path := deref(candidate); path != "" {
			return absoluteImageURL(path)
		}
	}
	return ""
}

func absoluteImageURL(path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	return imageBaseURL + strings.TrimPrefix(path, "/")
}

// indexIncluded splits the flat JSON:API included array into per-type lookups
// keyed by resource id.
func indexIncluded(included []library.IncludedItem) (map[string]*library.PublisherAttributes, map[string]*library.OrderProductInfo) {
	publishers := map[string]*library.PublisherAttributes{}
	products := map[string]*library.OrderProductInfo{}
	for _, entry := range included {
		if pub := entry.AsPublisher(); pub != nil {
			publishers[entry.ID] = pub
		}
		if prod := entry.AsProduct(); prod != nil {
			products[entry.ID] = prod
		}
	}
	return publishers, products
}

// relID reads the referenced resource id from one relationship, or "" when the
// relationship is absent.
func relID(rels *library.OrderProductRelationships, pick func(*library.OrderProductRelationships) *library.RelationshipRef) string {
	if rels == nil {
		return ""
	}
	ref := pick(rels)
	if ref == nil || ref.Data == nil {
		return ""
	}
	return ref.Data.ID
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}
