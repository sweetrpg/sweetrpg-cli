package cmd

import (
	"strings"

	"github.com/sweetrpg/catalog-objects.go/vo"
	modelcore "github.com/sweetrpg/model-core.go/vo"
	"github.com/sweetrpg/sweetrpg-cli/internal/client"
)

// Every def below references concrete vo fields in Set/Label/Detail, so
// catalog-objects schema changes fail this package's build.

func parseTags(vals []string) []modelcore.TagVO {
	out := make([]modelcore.TagVO, 0, len(vals))
	for _, spec := range vals {
		name, value := splitKV(spec)
		out = append(out, modelcore.TagVO{Name: name, Value: value})
	}
	return out
}

func parseProps(vals []string) []modelcore.PropertyVO {
	out := make([]modelcore.PropertyVO, 0, len(vals))
	for _, spec := range vals {
		name, value := splitKV(spec)
		out = append(out, modelcore.PropertyVO{Name: name, Kind: "string", Value: value})
	}
	return out
}

func textSetter[T any](field func(*T) *string) func(*T, []string) (any, error) {
	return func(v *T, vals []string) (any, error) {
		s, err := single("", vals)
		if err != nil {
			return nil, err
		}
		p := field(v)
		*p = s
		return s, nil
	}
}

func volumeDef() EntityDef[vo.VolumeVO] {
	return EntityDef[vo.VolumeVO]{
		Type:        client.Entities["volume"],
		PrimaryFlag: "title",
		PrimaryAttr: "title",
		PrimarySet:  func(v *vo.VolumeVO, s string) { v.Title = s },
		Label:       func(v *vo.VolumeVO) string { return v.Title },
		Detail:      func(v *vo.VolumeVO) string { return v.Format },
		Relations: []Relation{
			{Type: "system", WireName: "system", Many: true},
			{Type: "publisher", WireName: "publisher", Many: true},
			{Type: "studio", WireName: "studio", Many: true},
			{Type: "license", WireName: "license", Many: true},
		},
		Flags: []FlagDef[vo.VolumeVO]{
			{Name: "description", Attr: "description", Usage: "long description", Set: textSetter(func(v *vo.VolumeVO) *string { return &v.Description })},
			{Name: "notes", Attr: "notes", Usage: "internal notes", Set: textSetter(func(v *vo.VolumeVO) *string { return &v.Notes })},
			{Name: "format", Attr: "format", Usage: "physical/digital format (e.g. core)", Set: textSetter(func(v *vo.VolumeVO) *string { return &v.Format })},
			{Name: "tag", Attr: "tags", Repeated: true, Usage: "tag as name or name=value (repeatable)", Set: func(v *vo.VolumeVO, vals []string) (any, error) {
				v.Tags = parseTags(vals)
				return v.Tags, nil
			}},
			{Name: "property", Attr: "properties", Repeated: true, Usage: "property as name=value (repeatable)", Set: func(v *vo.VolumeVO, vals []string) (any, error) {
				v.Properties = parseProps(vals)
				return v.Properties, nil
			}},
		},
	}
}

func publisherDef() EntityDef[vo.PublisherVO] {
	return EntityDef[vo.PublisherVO]{
		Type:        client.Entities["publisher"],
		PrimaryFlag: "name",
		PrimaryAttr: "name",
		PrimarySet:  func(v *vo.PublisherVO, s string) { v.Name = s },
		Label:       func(p *vo.PublisherVO) string { return p.Name },
		Detail:      func(p *vo.PublisherVO) string { return p.Website },
		Flags: []FlagDef[vo.PublisherVO]{
			{Name: "address", Attr: "address", Usage: "postal address", Set: textSetter(func(v *vo.PublisherVO) *string { return &v.Address })},
			{Name: "website", Attr: "website", Usage: "website URL", Set: textSetter(func(v *vo.PublisherVO) *string { return &v.Website })},
			{Name: "notes", Attr: "notes", Usage: "internal notes", Set: textSetter(func(v *vo.PublisherVO) *string { return &v.Notes })},
			{Name: "tag", Attr: "tags", Repeated: true, Usage: "tag as name or name=value (repeatable)", Set: func(v *vo.PublisherVO, vals []string) (any, error) {
				v.Tags = parseTags(vals)
				return v.Tags, nil
			}},
			{Name: "property", Attr: "properties", Repeated: true, Usage: "property as name=value (repeatable)", Set: func(v *vo.PublisherVO, vals []string) (any, error) {
				v.Properties = parseProps(vals)
				return v.Properties, nil
			}},
		},
	}
}

func studioDef() EntityDef[vo.StudioVO] {
	return EntityDef[vo.StudioVO]{
		Type:        client.Entities["studio"],
		PrimaryFlag: "name",
		PrimaryAttr: "name",
		PrimarySet:  func(v *vo.StudioVO, s string) { v.Name = s },
		Label:       func(s *vo.StudioVO) string { return s.Name },
		Detail:      func(s *vo.StudioVO) string { return s.Website },
		Flags: []FlagDef[vo.StudioVO]{
			{Name: "website", Attr: "website", Usage: "website URL", Set: textSetter(func(v *vo.StudioVO) *string { return &v.Website })},
			{Name: "notes", Attr: "notes", Usage: "internal notes", Set: textSetter(func(v *vo.StudioVO) *string { return &v.Notes })},
			{Name: "tag", Attr: "tags", Repeated: true, Usage: "tag as name or name=value (repeatable)", Set: func(v *vo.StudioVO, vals []string) (any, error) {
				v.Tags = parseTags(vals)
				return v.Tags, nil
			}},
			{Name: "property", Attr: "properties", Repeated: true, Usage: "property as name=value (repeatable)", Set: func(v *vo.StudioVO, vals []string) (any, error) {
				v.Properties = parseProps(vals)
				return v.Properties, nil
			}},
		},
	}
}

func personDef() EntityDef[vo.PersonVO] {
	return EntityDef[vo.PersonVO]{
		Type:        client.Entities["person"],
		PrimaryFlag: "name",
		PrimaryAttr: "name",
		PrimarySet:  func(v *vo.PersonVO, s string) { v.Name = s },
		Label:       func(p *vo.PersonVO) string { return p.Name },
		Detail:      func(p *vo.PersonVO) string { return p.Notes },
		Flags: []FlagDef[vo.PersonVO]{
			{Name: "notes", Attr: "notes", Usage: "internal notes", Set: textSetter(func(v *vo.PersonVO) *string { return &v.Notes })},
			{Name: "tag", Attr: "tags", Repeated: true, Usage: "tag as name or name=value (repeatable)", Set: func(v *vo.PersonVO, vals []string) (any, error) {
				v.Tags = parseTags(vals)
				return v.Tags, nil
			}},
			{Name: "property", Attr: "properties", Repeated: true, Usage: "property as name=value (repeatable)", Set: func(v *vo.PersonVO, vals []string) (any, error) {
				v.Properties = parseProps(vals)
				return v.Properties, nil
			}},
		},
	}
}

func systemDef() EntityDef[vo.SystemVO] {
	return EntityDef[vo.SystemVO]{
		Type: client.Entities["system"],
		// GameSystem carries jsonapi attr "name" on the wire; json tag stays game_system.
		PrimaryFlag: "game-system",
		PrimaryAttr: "game_system",
		PrimarySet:  func(v *vo.SystemVO, s string) { v.GameSystem = s },
		Label:       func(s *vo.SystemVO) string { return s.GameSystem },
		Detail:      func(s *vo.SystemVO) string { return s.Edition },
		Flags: []FlagDef[vo.SystemVO]{
			{Name: "edition", Attr: "edition", Usage: "edition label (e.g. 5e)", Set: textSetter(func(v *vo.SystemVO) *string { return &v.Edition })},
			{Name: "notes", Attr: "notes", Usage: "internal notes", Set: textSetter(func(v *vo.SystemVO) *string { return &v.Notes })},
			{Name: "tag", Attr: "tags", Repeated: true, Usage: "tag as name or name=value (repeatable)", Set: func(v *vo.SystemVO, vals []string) (any, error) {
				v.Tags = parseTags(vals)
				return v.Tags, nil
			}},
		},
	}
}

func licenseDef() EntityDef[vo.LicenseVO] {
	return EntityDef[vo.LicenseVO]{
		Type:        client.Entities["license"],
		PrimaryFlag: "title",
		PrimaryAttr: "title",
		PrimarySet:  func(v *vo.LicenseVO, s string) { v.Title = s },
		Label:       func(l *vo.LicenseVO) string { return l.Title },
		Detail:      func(l *vo.LicenseVO) string { return l.Version },
		Flags: []FlagDef[vo.LicenseVO]{
			{Name: "short-title", Attr: "short_title", Usage: "abbreviated title", Set: textSetter(func(v *vo.LicenseVO) *string { return &v.ShortTitle })},
			{Name: "version", Attr: "version", Usage: "license version", Set: textSetter(func(v *vo.LicenseVO) *string { return &v.Version })},
			{Name: "deed", Attr: "deed", Usage: "deed URL", Set: textSetter(func(v *vo.LicenseVO) *string { return &v.Deed })},
			{Name: "legal-code", Attr: "legal_code", Usage: "legal code URL", Set: textSetter(func(v *vo.LicenseVO) *string { return &v.LegalCode })},
			{Name: "website", Attr: "website", Usage: "website URL", Set: textSetter(func(v *vo.LicenseVO) *string { return &v.Website })},
			{Name: "status", Attr: "status", Usage: "status (e.g. active, retired)", Set: textSetter(func(v *vo.LicenseVO) *string { return &v.Status })},
			{Name: "availability", Attr: "availability", Usage: "availability note", Set: textSetter(func(v *vo.LicenseVO) *string { return &v.Availability })},
			{Name: "notes", Attr: "notes", Usage: "internal notes", Set: textSetter(func(v *vo.LicenseVO) *string { return &v.Notes })},
			{Name: "tag", Attr: "tags", Repeated: true, Usage: "tag as name or name=value (repeatable)", Set: func(v *vo.LicenseVO, vals []string) (any, error) {
				v.Tags = parseTags(vals)
				return v.Tags, nil
			}},
			{Name: "property", Attr: "properties", Repeated: true, Usage: "property as name=value (repeatable)", Set: func(v *vo.LicenseVO, vals []string) (any, error) {
				v.Properties = parseProps(vals)
				return v.Properties, nil
			}},
		},
	}
}

func reviewDef() EntityDef[vo.ReviewVO] {
	return EntityDef[vo.ReviewVO]{
		Type:        client.Entities["review"],
		PrimaryFlag: "title",
		PrimaryAttr: "title",
		PrimarySet:  func(v *vo.ReviewVO, s string) { v.Title = s },
		Label:       func(r *vo.ReviewVO) string { return r.Title },
		Detail:      func(r *vo.ReviewVO) string { return r.Language },
		Relations:   []Relation{{Type: "volume", WireName: "volume"}},
		Flags: []FlagDef[vo.ReviewVO]{
			{Name: "body", Attr: "body", Usage: "review body text", Set: textSetter(func(v *vo.ReviewVO) *string { return &v.Body })},
			{Name: "language", Attr: "language", Usage: "review language (e.g. en-US)", Set: textSetter(func(v *vo.ReviewVO) *string { return &v.Language })},
			{Name: "notes", Attr: "notes", Usage: "internal notes", Set: textSetter(func(v *vo.ReviewVO) *string { return &v.Notes })},
			{Name: "tag", Attr: "tags", Repeated: true, Usage: "tag as name or name=value (repeatable)", Set: func(v *vo.ReviewVO, vals []string) (any, error) {
				v.Tags = parseTags(vals)
				return v.Tags, nil
			}},
		},
	}
}

func contributionDef() EntityDef[vo.ContributionVO] {
	return EntityDef[vo.ContributionVO]{
		Type:        client.Entities["contribution"],
		PrimaryFlag: "role",
		PrimaryAttr: "role",
		Label: func(c *vo.ContributionVO) string {
			var parts []string
			if c.Person != nil {
				parts = append(parts, c.Person.Name)
			}
			if c.Role != "" {
				parts = append(parts, c.Role)
			}
			if c.Volume != nil {
				parts = append(parts, c.Volume.Title)
			}
			return strings.Join(parts, " - ")
		},
		Detail:    func(c *vo.ContributionVO) string { return c.Notes },
		Relations: []Relation{{Type: "person", WireName: "person"}, {Type: "volume", WireName: "volume"}},
		Flags: []FlagDef[vo.ContributionVO]{
			{Name: "role", Attr: "role", Usage: "contribution role such as author or artist", Set: func(v *vo.ContributionVO, vals []string) (any, error) {
				role, err := single("role", vals)
				if err != nil {
					return nil, err
				}
				v.Role = role
				return v.Role, nil
			}},
			{Name: "notes", Attr: "notes", Usage: "internal notes", Set: textSetter(func(v *vo.ContributionVO) *string { return &v.Notes })},
		},
	}
}
