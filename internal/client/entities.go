package client

import "fmt"

// EntityType describes one catalog entity's URL shape. Searchable marks types
// that expose the /search?q= endpoint backing name resolution.
type EntityType struct {
	Name       string
	Plural     string
	Searchable bool
}

// Entities is the full registry of entity types the CLI can address.
var Entities = map[string]EntityType{
	"volume":       {Name: "volume", Plural: "volumes", Searchable: false},
	"publisher":    {Name: "publisher", Plural: "publishers", Searchable: true},
	"studio":       {Name: "studio", Plural: "studios", Searchable: true},
	"person":       {Name: "person", Plural: "persons", Searchable: true},
	"system":       {Name: "system", Plural: "systems", Searchable: true},
	"license":      {Name: "license", Plural: "licenses", Searchable: true},
	"review":       {Name: "review", Plural: "reviews", Searchable: false},
	"contribution": {Name: "contribution", Plural: "contributions", Searchable: false},
}

// Lookup returns the entity type for a CLI name, erroring on unknown types so
// typos fail before any network call.
func Lookup(name string) (EntityType, error) {
	t, ok := Entities[name]
	if !ok {
		return EntityType{}, fmt.Errorf("unknown entity type %q (known: volume, publisher, studio, person, system, license, review, contribution)", name)
	}
	return t, nil
}
