package cmd

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/spf13/pflag"
	"github.com/sweetrpg/catalog-cli/internal/client"
	modelcore "github.com/sweetrpg/model-core.go/vo"
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
			var records []*T
			var err error
			if def.Type.Searchable {
				records, err = client.Search[T](ctx, c, def.Type.Plural, q)
			} else {
				records, err = client.List[T](ctx, c, def.Type.Plural, client.ListOptions{
					Filters: []client.Filter{{Field: def.PrimaryAttr, Values: []string{q}}},
				})
			}
			if err != nil {
				return nil, err
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
	for rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return ""
	}
	return rv.FieldByName("ID").String()
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

func setTags(vals []string) (any, error) {
	out := make([]modelcore.TagVO, 0, len(vals))
	for _, spec := range vals {
		name, value := splitKV(spec)
		out = append(out, modelcore.TagVO{Name: name, Value: value})
	}
	return out, nil
}

func setProperties(vals []string) (any, error) {
	out := make([]modelcore.PropertyVO, 0, len(vals))
	for _, spec := range vals {
		name, value := splitKV(spec)
		out = append(out, modelcore.PropertyVO{Name: name, Kind: "string", Value: value})
	}
	return out, nil
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
