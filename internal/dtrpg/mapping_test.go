package dtrpg

import (
	"encoding/json"
	"testing"

	"github.com/pilgrimagesoftware/dtrpg-sdk.go/library"
	modelcore "github.com/sweetrpg/model-core.go/vo"
)

func strptr(s string) *string { return &s }

func propMap(props []modelcore.PropertyVO) map[string]string {
	m := map[string]string{}
	for _, p := range props {
		m[p.Name] = p.Value
	}
	return m
}

func TestMapProductsEmbeddedMetadata(t *testing.T) {
	lib := Library{Products: []library.OrderProductItem{{
		ID:           "1",
		ResourceType: "OrderProduct",
		Attributes: library.OrderProductAttributes{
			ProductID:      42,
			OrderProductID: 900,
			Name:           "Dungeon World",
			ISBN:           strptr("978-1-000-00000-0"),
			DatePurchased:  strptr("2021-04-05"),
			Archived:       0,
			Publisher:      &library.OrderProductPublisher{Name: "Sage Kobold Productions"},
			Product: &library.OrderProductInfo{
				WebImage:    strptr("products/covers/dw.jpg"),
				Description: &library.OrderProductDescription{ShortDescription: strptr("A fantasy world of adventure.")},
			},
			Filters: []library.OrderProductFilter{
				{Name: "Fantasy"}, {Name: "Fantasy"}, {Name: "Powered by the Apocalypse"}, {Name: ""},
			},
		},
	}}}

	got := MapProducts(lib)
	if len(got) != 1 {
		t.Fatalf("got %d products, want 1", len(got))
	}
	p := got[0]
	if p.ProductID != "42" || p.Title != "Dungeon World" || p.Archived {
		t.Fatalf("unexpected product summary: %+v", p)
	}
	if p.PublisherName != "Sage Kobold Productions" {
		t.Errorf("publisher = %q", p.PublisherName)
	}
	if p.Volume.Description != "A fantasy world of adventure." {
		t.Errorf("description = %q", p.Volume.Description)
	}
	if len(p.Volume.Tags) != 2 || p.Volume.Tags[0].Name != "Fantasy" || p.Volume.Tags[1].Name != "Powered by the Apocalypse" {
		t.Errorf("tags = %+v, want deduped [Fantasy, Powered by the Apocalypse]", p.Volume.Tags)
	}
	if p.CoverURL != imageBaseURL+"products/covers/dw.jpg" {
		t.Errorf("cover url = %q", p.CoverURL)
	}
	props := propMap(p.Volume.Properties)
	if props[PropProductID] != "42" || props[PropISBN] != "978-1-000-00000-0" {
		t.Errorf("properties = %+v", props)
	}
	for _, gone := range []string{"dtrpg_order_product_id", "dtrpg_purchase_date", "dtrpg_cover_url"} {
		if _, ok := props[gone]; ok {
			t.Errorf("property %q should not be recorded", gone)
		}
	}
}

func TestMapProductsResolvesSideloadedPublisherAndProduct(t *testing.T) {
	pubAttrs, _ := json.Marshal(library.PublisherAttributes{Name: "Evil Hat Productions", PublisherID: 7})
	prodAttrs, _ := json.Marshal(library.OrderProductInfo{
		ProductID:   55,
		Image:       strptr("https://cdn.example/cover.png"),
		Description: &library.OrderProductDescription{ShortDescription: strptr("Fate Core System.")},
	})

	lib := Library{
		Products: []library.OrderProductItem{{
			ID:           "2",
			ResourceType: "OrderProduct",
			Attributes: library.OrderProductAttributes{
				ProductID:      55,
				OrderProductID: 901,
				Name:           "Fate Core",
				Archived:       1,
			},
			Relationships: &library.OrderProductRelationships{
				Publisher: &library.RelationshipRef{Data: &library.RelationshipData{ResourceType: "Publisher", ID: "p7"}},
				Product:   &library.RelationshipRef{Data: &library.RelationshipData{ResourceType: "Product", ID: "pr55"}},
			},
		}},
		Included: []library.IncludedItem{
			{ID: "p7", ResourceType: "Publisher", Attributes: pubAttrs},
			{ID: "pr55", ResourceType: "Product", Attributes: prodAttrs},
		},
	}

	p := MapProducts(lib)[0]
	if !p.Archived {
		t.Error("expected archived product")
	}
	if p.PublisherName != "Evil Hat Productions" {
		t.Errorf("publisher = %q, want sideloaded name", p.PublisherName)
	}
	if p.Volume.Description != "Fate Core System." {
		t.Errorf("description = %q", p.Volume.Description)
	}
	if p.CoverURL != "https://cdn.example/cover.png" {
		t.Errorf("cover url = %q, want absolute passthrough", p.CoverURL)
	}
}

func TestMapProductsOmitsBlankProperties(t *testing.T) {
	lib := Library{Products: []library.OrderProductItem{{
		Attributes: library.OrderProductAttributes{ProductID: 1, OrderProductID: 2, Name: "Bare"},
	}}}
	p := MapProducts(lib)[0]
	props := propMap(p.Volume.Properties)
	if _, ok := props[PropISBN]; ok {
		t.Error("blank ISBN should be omitted")
	}
	if p.CoverURL != "" {
		t.Errorf("missing cover should yield empty CoverURL, got %q", p.CoverURL)
	}
	if props[PropProductID] != "1" {
		t.Errorf("product id property = %q", props[PropProductID])
	}
}
