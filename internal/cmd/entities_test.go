package cmd

import (
	"reflect"
	"testing"

	"github.com/sweetrpg/catalog-cli/internal/client"
	"github.com/sweetrpg/catalog-objects.go/vo"
)

func TestRegistryCoversAllEntities(t *testing.T) {
	want := []string{"volume", "publisher", "studio", "person", "system", "license", "review", "contribution"}
	if len(entityRegistry) != len(want) {
		t.Fatalf("registry has %d entries, want %d", len(entityRegistry), len(want))
	}
	for _, name := range want {
		ops, ok := entityRegistry[name]
		if !ok {
			t.Fatalf("entity %q missing from registry", name)
		}
		if ops.spec.Name != name || ops.spec.Plural == "" {
			t.Errorf("entity %q has bad spec %+v", name, ops.spec)
		}
	}
}

func TestVolumeSettersPopulateFields(t *testing.T) {
	ops := entityRegistry["volume"]
	rec, err := ops.buildCreate(map[string][]string{
		"description": {"A dungeon world"},
		"format":      {"core"},
		"tag":         {"setting=fantasy", "kickstarter"},
	})
	if err != nil {
		t.Fatal(err)
	}
	v, ok := rec.(*vo.VolumeVO)
	if !ok {
		t.Fatalf("buildCreate returned %T", rec)
	}
	if v.Description != "A dungeon world" || v.Format != "core" {
		t.Errorf("scalar setters missed fields: %+v", v)
	}
	wantTags := map[string]string{"setting": "fantasy", "kickstarter": ""}
	for _, tag := range v.Tags {
		if wantTags[tag.Name] != tag.Value {
			t.Errorf("tag %q=%q unexpected", tag.Name, tag.Value)
		}
	}
	if len(v.Tags) != 2 {
		t.Errorf("got %d tags, want 2", len(v.Tags))
	}
}

func TestPropertySetterDefaultsKind(t *testing.T) {
	ops := entityRegistry["publisher"]
	rec, err := ops.buildCreate(map[string][]string{"property": {"founded=2005"}})
	if err != nil {
		t.Fatal(err)
	}
	p := rec.(*vo.PublisherVO)
	if len(p.Properties) != 1 || p.Properties[0].Name != "founded" ||
		p.Properties[0].Value != "2005" || p.Properties[0].Kind != "string" {
		t.Errorf("property not parsed as expected: %+v", p.Properties)
	}
}

func TestPatchPayloadKeysMatchWireAttrs(t *testing.T) {
	cases := []struct {
		entity   string
		values   map[string][]string
		attrKeys []string // keys expected in patch body
	}{
		{"volume", map[string][]string{"format": {"core"}, "notes": {"n"}}, []string{"format", "notes"}},
		{"system", map[string][]string{"game-system": {"D&D"}, "edition": {"5e"}}, []string{"game_system", "edition"}},
		{"license", map[string][]string{"short-title": {"CC-BY"}}, []string{"short_title"}},
		{"contribution", map[string][]string{"roles": {"author", "artist"}}, []string{"Roles"}},
	}
	for _, tc := range cases {
		fields, err := entityRegistry[tc.entity].buildPatch(tc.values)
		if err != nil {
			t.Fatalf("%s: %v", tc.entity, err)
		}
		if len(fields) != len(tc.attrKeys) {
			t.Errorf("%s: got %d patch fields %v, want %d", tc.entity, len(fields), fields, len(tc.attrKeys))
			continue
		}
		for _, k := range tc.attrKeys {
			if _, ok := fields[k]; !ok {
				t.Errorf("%s: patch missing key %q in %v", tc.entity, k, fields)
			}
		}
	}
}

func TestPatchIgnoresUnsetFlags(t *testing.T) {
	fields, err := entityRegistry["studio"].buildPatch(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 0 {
		t.Errorf("expected empty patch, got %v", fields)
	}
}

func TestPatchRejectsRepeatedScalar(t *testing.T) {
	if _, err := entityRegistry["volume"].buildPatch(map[string][]string{"format": {"a", "b"}}); err == nil {
		t.Fatal("expected error for repeated scalar flag")
	}
}

func TestLabelAndDetailErase(t *testing.T) {
	vol := &vo.VolumeVO{Title: "Curse of Strahd"}
	sys := &vo.SystemVO{GameSystem: "D&D 5e", Edition: "5e"}
	contrib := &vo.ContributionVO{
		Person: &vo.PersonVO{Name: "John Wick"},
		Roles:  []string{"author"},
		Volume: &vo.VolumeVO{Title: "7th Sea"},
	}

	if got := entityRegistry["volume"].labelOf(vol); got != "Curse of Strahd" {
		t.Errorf("volume label = %q", got)
	}
	if got := entityRegistry["system"].detailOf(sys); got != "5e" {
		t.Errorf("system detail = %q", got)
	}
	if got := entityRegistry["contribution"].labelOf(contrib); got != "John Wick - author - 7th Sea" {
		t.Errorf("contribution label = %q", got)
	}
}

func TestVoIDExtraction(t *testing.T) {
	if id := voID(&vo.LicenseVO{ID: "abc123"}); id != "abc123" {
		t.Errorf("voID = %q", id)
	}
	if id := voID(nil); id != "" {
		t.Errorf("voID(nil) = %q", id)
	}
}

func TestRelationTablesMatchModel(t *testing.T) {
	cases := map[string][]Relation{
		"volume":       {{"system", "system", true}, {"publisher", "publisher", true}, {"studio", "studio", true}, {"license", "license", true}},
		"review":       {{"volume", "volume", false}},
		"contribution": {{"person", "person", false}, {"volume", "volume", false}},
	}
	for name, want := range cases {
		got := relationsFor(name)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s relations = %v, want %v", name, got, want)
		}
	}
}

func relationsFor(name string) []Relation {
	defs := map[string]func() []Relation{
		"volume":       func() []Relation { return volumeDef().Relations },
		"review":       func() []Relation { return reviewDef().Relations },
		"contribution": func() []Relation { return contributionDef().Relations },
		"publisher":    func() []Relation { return nil },
		"studio":       func() []Relation { return nil },
		"person":       func() []Relation { return nil },
		"system":       func() []Relation { return nil },
		"license":      func() []Relation { return nil },
	}
	return defs[name]()
}

func TestSearchableRoutingMatchesClientRegistry(t *testing.T) {
	for name, ops := range entityRegistry {
		if ops.spec.Searchable != client.Entities[name].Searchable {
			t.Errorf("%s searchable mismatch between registry copies", name)
		}
	}
}
