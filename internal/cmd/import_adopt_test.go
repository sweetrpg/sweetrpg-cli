package cmd

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	vo "github.com/sweetrpg/catalog-objects.go/vo"
	modelcore "github.com/sweetrpg/model-core.go/vo"
	"github.com/sweetrpg/sweetrpg-cli/internal/client"
	"github.com/sweetrpg/sweetrpg-cli/internal/dtrpg"
)

type fakeCatalog struct {
	volumes   []*vo.VolumeVO
	searchErr error
	queries   []string
	patchErr  error
	patchDisp client.WriteDisposition
	patched   map[string]map[string]any
}

func (f *fakeCatalog) SearchVolumes(_ context.Context, q string) ([]*vo.VolumeVO, error) {
	f.queries = append(f.queries, q)
	return f.volumes, f.searchErr
}

func (f *fakeCatalog) PatchVolume(_ context.Context, id string, fields map[string]any) (client.WriteDisposition, error) {
	if f.patched == nil {
		f.patched = map[string]map[string]any{}
	}
	f.patched[id] = fields
	return f.patchDisp, f.patchErr
}

func publishers(known map[string]string) publisherLookup {
	return func(_ context.Context, name string) (string, bool, error) {
		id, ok := known[strings.ToLower(strings.TrimSpace(name))]
		return id, ok, nil
	}
}

func product(id, title, publisher string) dtrpg.Product {
	return dtrpg.Product{
		ProductID:     id,
		Title:         title,
		PublisherName: publisher,
		Volume: vo.VolumeVO{
			Title:      title,
			Properties: []modelcore.PropertyVO{{Name: dtrpg.PropProductID, Value: id}},
		},
	}
}

func volume(id, title string, publisherIDs ...string) *vo.VolumeVO {
	v := &vo.VolumeVO{ID: id, Title: title}
	for _, p := range publisherIDs {
		v.Publishers = append(v.Publishers, &vo.PublisherVO{ID: p})
	}
	return v
}

func claimed(v *vo.VolumeVO, productID string) *vo.VolumeVO {
	v.Properties = []modelcore.PropertyVO{{Name: dtrpg.PropProductID, Value: productID}}
	return v
}

func TestNormalizeName(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"case", "A Glorious Death", "a glorious death"},
		{"punctuation", "A Glorious Death!", "a glorious death"},
		{"hyphen and spacing", "  A  Glorious-Death ", "a glorious death"},
		{"full-width NFKC", "ＡＢＣ Ｄｅａｔｈ", "abc death"},
		{"ligature NFKC", "ﬁre Keep", "fire keep"},
		{"apostrophes", "Dragon's Hoard: Vol. 2", "dragon s hoard vol 2"},
		{"empty", "  ...  ", ""},
		{"unicode letters", "Ça Ira", "ça ira"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeName(tt.in); got != tt.want {
				t.Errorf("normalizeName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSearchToken(t *testing.T) {
	tests := []struct{ in, want string }{
		{"a glorious death", "glorious"},
		{"fate", "fate"},
		{"", ""},
		{"of the sword", "sword"},
	}
	for _, tt := range tests {
		if got := searchToken(tt.in); got != tt.want {
			t.Errorf("searchToken(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestPlanMatches(t *testing.T) {
	pubs := publishers(map[string]string{"sage kobold productions": "pub-1", "other press": "pub-2"})
	tests := []struct {
		name          string
		prod          dtrpg.Product
		volumes       []*vo.VolumeVO
		wantCreate    int
		wantAdoptVol  string
		wantAmbiguous []string
	}{
		{"no candidates creates", product("1", "A Glorious Death", "Sage Kobold Productions"), nil, 1, "", nil},
		{"one legacy volume adopted",
			product("1", "A Glorious Death", "Sage Kobold Productions"),
			[]*vo.VolumeVO{volume("v1", "a glorious death!", "pub-1")}, 0, "v1", nil},
		{"title differs creates",
			product("1", "A Glorious Death", "Sage Kobold Productions"),
			[]*vo.VolumeVO{volume("v1", "A Glorious Death II", "pub-1")}, 1, "", nil},
		{"other publisher creates",
			product("1", "A Glorious Death", "Sage Kobold Productions"),
			[]*vo.VolumeVO{volume("v1", "A Glorious Death", "pub-2")}, 1, "", nil},
		{"volume without publisher does not match a named publisher",
			product("1", "A Glorious Death", "Sage Kobold Productions"),
			[]*vo.VolumeVO{volume("v1", "A Glorious Death")}, 1, "", nil},
		{"unknown publisher creates",
			product("1", "A Glorious Death", "Brand New Press"),
			[]*vo.VolumeVO{volume("v1", "A Glorious Death", "pub-1")}, 1, "", nil},
		{"product without publisher matches publisher-less volume",
			product("1", "A Glorious Death", ""),
			[]*vo.VolumeVO{volume("v1", "A Glorious Death"), volume("v2", "A Glorious Death", "pub-1")}, 0, "v1", nil},
		{"volume with another product ID is never adopted",
			product("1", "A Glorious Death", "Sage Kobold Productions"),
			[]*vo.VolumeVO{claimed(volume("v1", "A Glorious Death", "pub-1"), "999")}, 1, "", nil},
		{"two candidates are ambiguous",
			product("1", "A Glorious Death", "Sage Kobold Productions"),
			[]*vo.VolumeVO{volume("v1", "A Glorious Death", "pub-1"), volume("v2", "A glorious death", "pub-1")},
			0, "", []string{"v1", "v2"}},
		{"duplicate search hits count once",
			product("1", "A Glorious Death", "Sage Kobold Productions"),
			[]*vo.VolumeVO{volume("v1", "A Glorious Death", "pub-1"), volume("v1", "A Glorious Death", "pub-1")}, 0, "v1", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := &fakeCatalog{volumes: tt.volumes}
			plan, err := planMatches(context.Background(), fc, pubs, []dtrpg.Product{tt.prod})
			if err != nil {
				t.Fatalf("planMatches: %v", err)
			}
			if len(plan.toCreate) != tt.wantCreate {
				t.Errorf("toCreate = %d, want %d", len(plan.toCreate), tt.wantCreate)
			}
			if tt.wantAdoptVol == "" && len(plan.toAdopt) != 0 {
				t.Errorf("unexpected adoption of %s", plan.toAdopt[0].volume.ID)
			}
			if tt.wantAdoptVol != "" && (len(plan.toAdopt) != 1 || plan.toAdopt[0].volume.ID != tt.wantAdoptVol) {
				t.Errorf("toAdopt = %+v, want volume %s", plan.toAdopt, tt.wantAdoptVol)
			}
			var gotAmb []string
			for _, a := range plan.ambiguous {
				gotAmb = append(gotAmb, a.volumeIDs...)
			}
			if !reflect.DeepEqual(gotAmb, tt.wantAmbiguous) {
				t.Errorf("ambiguous = %v, want %v", gotAmb, tt.wantAmbiguous)
			}
		})
	}
}

func TestPlanMatchesSearchesByTitleToken(t *testing.T) {
	fc := &fakeCatalog{}
	_, err := planMatches(context.Background(), fc, publishers(nil), []dtrpg.Product{product("1", "A Glorious Death", "")})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fc.queries, []string{"glorious"}) {
		t.Errorf("queries = %v, want [glorious]", fc.queries)
	}
}

func TestPlanMatchesTwoProductsWantingOneVolumeAreBothAmbiguous(t *testing.T) {
	fc := &fakeCatalog{volumes: []*vo.VolumeVO{volume("v1", "Fate Core", "pub-1")}}
	prods := []dtrpg.Product{product("1", "Fate Core", "Evil Hat"), product("2", "FATE CORE", "Evil Hat")}
	plan, err := planMatches(context.Background(), fc, publishers(map[string]string{"evil hat": "pub-1"}), prods)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.toAdopt) != 0 || len(plan.ambiguous) != 2 || len(plan.toCreate) != 0 {
		t.Errorf("plan = %+v, want 2 ambiguous only", plan)
	}
}

func TestPlanMatchesSearchErrorAbortsPlan(t *testing.T) {
	fc := &fakeCatalog{searchErr: errors.New("catalog down")}
	_, err := planMatches(context.Background(), fc, publishers(nil), []dtrpg.Product{product("1", "Fate Core", "")})
	if err == nil || !strings.Contains(err.Error(), "catalog down") {
		t.Fatalf("err = %v, want catalog down", err)
	}
}

func TestAdoptionFieldsAreAdditive(t *testing.T) {
	v := volume("v1", "A Glorious Death")
	v.Properties = []modelcore.PropertyVO{{Name: "isbn", Value: "123"}, {Name: dtrpg.PropProductID, Value: ""}}
	fields, err := adoptionFields(v, product("191454", "A Glorious Death", ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 1 {
		t.Errorf("fields = %v, want only properties (title, description, tags, cover untouched)", fields)
	}
	want := []modelcore.PropertyVO{{Name: "isbn", Value: "123"}, {Name: dtrpg.PropProductID, Value: "191454"}}
	if got := fields["properties"]; !reflect.DeepEqual(got, want) {
		t.Errorf("properties = %v, want %v", got, want)
	}
}

func TestAdoptVolume(t *testing.T) {
	tests := []struct {
		name    string
		disp    client.WriteDisposition
		patch   error
		wantErr string
	}{
		{"applied", client.WriteDisposition{}, nil, ""},
		{"rejected for non-editor", client.WriteDisposition{}, errors.New("400 invalid_request"), "400"},
		{"queued for review", client.WriteDisposition{Submitted: true, Version: 3}, nil, "queued the change for review"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := &fakeCatalog{patchDisp: tt.disp, patchErr: tt.patch}
			err := adoptVolume(context.Background(), fc, adoption{prod: product("7", "Fate", ""), volume: volume("v1", "Fate")})
			if tt.wantErr == "" && err != nil {
				t.Fatalf("err = %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
			if _, ok := fc.patched["v1"]; !ok {
				t.Error("no PATCH sent to the legacy volume")
			}
		})
	}
}

func TestDTRPGLibraryAdoptsLegacyVolume(t *testing.T) {
	legacy := `{"data":[{"type":"volume","id":"vol-legacy","attributes":{"title":"Fate Core"},` +
		`"relationships":{"publisher":{"data":[{"type":"publisher","id":"pub-existing"}]}}}]}`
	stub := &catalogStub{
		volumesJSON:    legacy,
		publishersJSON: `{"data":[{"type":"publisher","id":"pub-existing","attributes":{"name":"Evil Hat Productions"}}]}`,
	}
	setupImportLibrary(t, stub, dtrpgLibraryServer(t, dtrpgProductJSON(1, "Fate Core", "Evil Hat Productions", 0)), true)

	out, err := runImportChild(t, "library")
	if err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	if stub.volumePOSTs != 0 {
		t.Errorf("created %d volumes, want 0 (adopted)", stub.volumePOSTs)
	}
	if stub.patches < 1 {
		t.Errorf("patches = %d, want the adoption PATCH", stub.patches)
	}
	if !strings.Contains(out, "1 adopted") {
		t.Errorf("summary missing adopted count:\n%s", out)
	}
}

func TestDTRPGLibraryDryRunListsAdoptionsAndAmbiguous(t *testing.T) {
	two := `{"data":[` +
		`{"type":"volume","id":"vol-a","attributes":{"title":"Fate Core"},"relationships":{"publisher":{"data":[{"type":"publisher","id":"pub-existing"}]}}},` +
		`{"type":"volume","id":"vol-b","attributes":{"title":"fate core"},"relationships":{"publisher":{"data":[{"type":"publisher","id":"pub-existing"}]}}}]}`
	stub := &catalogStub{
		volumesJSON:    two,
		publishersJSON: `{"data":[{"type":"publisher","id":"pub-existing","attributes":{"name":"Evil Hat Productions"}}]}`,
	}
	setupImportLibrary(t, stub, dtrpgLibraryServer(t, dtrpgProductJSON(1, "Fate Core", "Evil Hat Productions", 0)), true)
	flagImportDryRun = true

	out, err := runImportChild(t, "library")
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	for _, want := range []string{"ambiguous (skipped)", "vol-a", "vol-b", "1 ambiguous"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output missing %q:\n%s", want, out)
		}
	}
	if stub.volumePOSTs != 0 || stub.patches != 0 || stub.publisherPOSTs != 0 {
		t.Errorf("dry run wrote: volumes=%d patches=%d publishers=%d", stub.volumePOSTs, stub.patches, stub.publisherPOSTs)
	}
}
