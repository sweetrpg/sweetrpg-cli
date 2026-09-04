package cmd

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/spf13/pflag"
	"github.com/sweetrpg/sweetrpg-cli/internal/client"
)

// Relation names one modeled connection to another entity type. WireName is
// the JSON:API relation tag on the owning VO; Many marks array relations.
type Relation struct {
	Type     string // counterpart entity type name
	WireName string // relation key on the wire, e.g. "publisher"
	Many     bool
}

// FlagDef binds one CLI property flag to a VO field. Set writes the field
// directly and returns the wire value for PATCH bodies, so renaming or
// removing that field in catalog-objects breaks the build here - the schema-
// drift check the design calls for.
type FlagDef[T any] struct {
	Name     string // flag spelling, e.g. "short-title"
	Attr     string // payload key sent to catalog-api (json tag)
	Usage    string
	Repeated bool // flag may appear multiple times; value is a slice
	Set      func(v *T, vals []string) (any, error)
}

// EntityDef describes one catalog entity type for the generic commands.
type EntityDef[T any] struct {
	Type        client.EntityType
	PrimaryFlag string           // positional-name flag, e.g. "title"
	PrimaryAttr string           // its payload key
	PrimarySet  func(*T, string) // writes the positional name onto the VO
	Flags       []FlagDef[T]
	Relations   []Relation
	Label       func(*T) string // display label for pickers and output
	Detail      func(*T) string // distinguishing info shown beside labels
}

// candidate is a type-erased resolution hit.
type candidate struct {
	ID     string
	Label  string
	Detail string
}

// entityOps is the type-erased surface generic commands drive. Everything
// closes over concrete vo types from adapt(), keeping call sites untyped.
type entityOps struct {
	spec        client.EntityType
	primaryFlag string
	register    func(*pflag.FlagSet)
	buildCreate func(values map[string][]string) (any, error)
	create      func(context.Context, *client.Client, any) (*string, error)
	buildPatch  func(values map[string][]string) (map[string]any, error)
	patch       func(context.Context, *client.Client, string, map[string]any) (any, *client.WriteDisposition, error)
	get         func(context.Context, *client.Client, string) (any, error)
	del         func(context.Context, *client.Client, string) error
	find        func(context.Context, *client.Client, string) ([]candidate, error)
	labelOf     func(any) string
	detailOf    func(any) string
}

var entityRegistry = map[string]entityOps{}

func register[T any](def EntityDef[T]) {
	if _, dup := entityRegistry[def.Type.Name]; dup {
		panic(fmt.Sprintf("entity %q registered twice", def.Type.Name))
	}
	entityRegistry[def.Type.Name] = adapt(def)
}

func adapt[T any](def EntityDef[T]) entityOps {
	// Contribution's primary ("roles") is also a repeated flag; when that
	// happens the FlagDef owns create+patch handling and the primary block
	// stays out of the way.
	primaryIsFlag := false
	for _, f := range def.Flags {
		if f.Name == def.PrimaryFlag {
			primaryIsFlag = true
		}
	}
	return entityOps{
		spec:        def.Type,
		primaryFlag: def.PrimaryFlag,
		register: func(fs *pflag.FlagSet) {
			for _, f := range def.Flags {
				if f.Repeated {
					fs.StringArray(f.Name, nil, f.Usage)
				} else {
					fs.String(f.Name, "", f.Usage)
				}
			}
		},
		buildCreate: func(values map[string][]string) (any, error) {
			v := new(T)
			if !primaryIsFlag {
				if vals := values[def.PrimaryFlag]; len(vals) > 0 {
					s, err := single(def.PrimaryFlag, vals)
					if err != nil {
						return nil, fmt.Errorf("--%s: %w", def.PrimaryFlag, err)
					}
					def.PrimarySet(v, s)
				}
			}
			if _, err := applyFlags(def.Flags, v, values); err != nil {
				return nil, err
			}
			return v, nil
		},
		create: func(ctx context.Context, c *client.Client, rec any) (*string, error) {
			typed, ok := rec.(*T)
			if !ok {
				return nil, fmt.Errorf("internal: unexpected create payload %T", rec)
			}
			created, err := client.Create(ctx, c, def.Type.Plural, typed)
			if err != nil {
				return nil, err
			}
			id := voID(created)
			return &id, nil
		},
		buildPatch: func(values map[string][]string) (map[string]any, error) {
			fields := map[string]any{}
			for _, f := range def.Flags {
				vals, ok := values[f.Name]
				if !ok || len(vals) == 0 {
					continue
				}
				wire, err := f.Set(new(T), vals)
				if err != nil {
					return nil, fmt.Errorf("--%s: %w", f.Name, err)
				}
				fields[f.Attr] = wire
			}
			if !primaryIsFlag {
				if vals := values[def.PrimaryFlag]; len(vals) > 0 {
					s, err := single(def.PrimaryFlag, vals)
					if err != nil {
						return nil, fmt.Errorf("--%s: %w", def.PrimaryFlag, err)
					}
					fields[def.PrimaryAttr] = s
				}
			}
			return fields, nil
		},
		patch: func(ctx context.Context, c *client.Client, id string, fields map[string]any) (any, *client.WriteDisposition, error) {
			updated, disp, err := client.Patch[T](ctx, c, def.Type.Plural, id, fields)
			if err != nil {
				return nil, nil, err
			}
			return updated, &disp, nil
		},
		get: func(ctx context.Context, c *client.Client, id string) (any, error) {
			return client.Get[T](ctx, c, def.Type.Plural, id)
		},
		del: func(ctx context.Context, c *client.Client, id string) error {
			return client.Delete(ctx, c, def.Type.Plural, id)
		},
		find: func(ctx context.Context, c *client.Client, q string) ([]candidate, error) {
			records, err := fetchForFind[T](ctx, c, def.Type, q)
			if err != nil {
				return nil, err
			}
			if !def.Type.Searchable {
				records = pickMatches(records, def.PrimaryAttr, def.Label, q)
			}
			out := make([]candidate, 0, len(records))
			for _, r := range records {
				out = append(out, candidate{ID: voID(r), Label: def.Label(r), Detail: def.Detail(r)})
			}
			return out, nil
		},
		labelOf: func(a any) string {
			rec, ok := a.(*T)
			if !ok {
				return fmt.Sprintf("%T", a)
			}
			return def.Label(rec)
		},
		detailOf: func(a any) string {
			rec, _ := a.(*T)
			return def.Detail(rec)
		},
	}
}

// applyFlags runs each provided flag's setter onto v, failing on bad input
// before anything reaches the API.
func applyFlags[T any](defs []FlagDef[T], v *T, values map[string][]string) (map[string]any, error) {
	wire := map[string]any{}
	for _, f := range defs {
		vals, ok := values[f.Name]
		if !ok || len(vals) == 0 {
			continue
		}
		w, err := f.Set(v, vals)
		if err != nil {
			return nil, fmt.Errorf("--%s: %w", f.Name, err)
		}
		wire[f.Attr] = w
	}
	return wire, nil
}

// voID reads the record's ID regardless of concrete VO type; every VO leads
// with an ID field by JSON:API convention.
func voID(v any) string {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return ""
	}
	return rv.FieldByName("ID").String()
}

// fetchForFind gathers the records a query matches against. Searchable types
// delegate to catalog-api's /search endpoint; the rest page through the whole
// collection because server-side filters are exact $eq matches - far too
// strict for name lookup.
func fetchForFind[T any](ctx context.Context, c *client.Client, spec client.EntityType, q string) ([]*T, error) {
	if spec.Searchable {
		return client.Search[T](ctx, c, spec.Plural, q)
	}
	const pageSize = 500
	const maxRecords = 10000
	all := make([]*T, 0)
	for start := 0; len(all) < maxRecords; start += pageSize {
		page, err := client.List[T](ctx, c, spec.Plural, client.ListOptions{Start: start, Limit: pageSize})
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < pageSize {
			return all, nil
		}
	}
	return all, nil
}

// pickMatches filters records to those whose primary attribute (or label,
// when the attribute is not a plain string) case-insensitively equals the
// query; when nothing matches exactly, substring hits are returned instead.
func pickMatches[T any](records []*T, attr string, label func(*T) string, query string) []*T {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	var exact, partial []*T
	for _, r := range records {
		hay := voString(r, attr)
		if hay == "" {
			hay = label(r)
		}
		lower := strings.ToLower(hay)
		switch {
		case lower == q:
			exact = append(exact, r)
		case strings.Contains(lower, q):
			partial = append(partial, r)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return partial
}

// voString reads the struct field whose json tag names attr, when it holds a
// string; non-string attributes yield "" so callers can fall back to labels.
func voString(v any, attr string) string {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return ""
	}
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		tag := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]
		if tag != attr {
			continue
		}
		if fv := rv.Field(i); fv.Kind() == reflect.String {
			return fv.String()
		}
		return ""
	}
	return ""
}

// single returns the lone value for a single-shot flag.
func single(flag string, vals []string) (string, error) {
	if len(vals) != 1 {
		return "", fmt.Errorf("expected one value, got %d", len(vals))
	}
	return vals[0], nil
}

// splitKV parses "name=value" (or bare "name") pairs used by --tag/--property.
func splitKV(spec string) (string, string) {
	if k, v, found := strings.Cut(spec, "="); found {
		return k, v
	}
	return spec, ""
}

func init() {
	register(volumeDef())
	register(publisherDef())
	register(studioDef())
	register(personDef())
	register(systemDef())
	register(licenseDef())
	register(reviewDef())
	register(contributionDef())
}
