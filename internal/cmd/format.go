package cmd

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

type outputFormat int

const (
	formatHuman outputFormat = iota
	formatJSON
	formatYAML
)

// changedFlag reports whether a boolean flag was explicitly provided.
func changedFlag(cmd *cobra.Command, name string) bool {
	fl := cmd.Flags().Lookup(name)
	return fl != nil && fl.Changed && fl.Value.String() == "true"
}

// formatFromFlags resolves the --json/--yaml pair into one output format.
func formatFromFlags(cmd *cobra.Command) (outputFormat, error) {
	jsonFlag := changedFlag(cmd, "json")
	yamlFlag := changedFlag(cmd, "yaml")
	switch {
	case jsonFlag && yamlFlag:
		return formatHuman, usageErr("--json and --yaml are mutually exclusive")
	case jsonFlag:
		return formatJSON, nil
	case yamlFlag:
		return formatYAML, nil
	default:
		return formatHuman, nil
	}
}

// printRecord renders a record in the requested format.
func printRecord(cmd *cobra.Command, rec any, format outputFormat) error {
	var (
		out []byte
		err error
	)
	switch format {
	case formatJSON:
		out, err = json.MarshalIndent(rec, "", "  ")
		out = append(out, '\n')
	case formatYAML:
		out, err = yaml.Marshal(rec)
	default:
		out = []byte(humanRecord(rec))
	}
	if err != nil {
		return err
	}
	_, err = cmd.OutOrStdout().Write(out)
	return err
}

// humanRecord renders struct fields as aligned key: value lines, skipping
// empty values; slices of structs expand one item per line.
func humanRecord(rec any) string {
	var b strings.Builder
	writeStruct(&b, reflect.ValueOf(rec), 0)
	return b.String()
}

func writeStruct(b *strings.Builder, v reflect.Value, indent int) {
	for v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return
	}
	t := v.Type()
	pad := strings.Repeat("  ", indent)
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		key := field.Name
		if tag := field.Tag.Get("json"); tag != "" && tag != "-" {
			key = strings.Split(tag, ",")[0]
		}
		fv := v.Field(i)
		if field.Anonymous && fv.Kind() == reflect.Struct {
			writeStruct(b, fv, indent)
			continue
		}
		if isEmptyValue(fv) {
			continue
		}
		if fv.Kind() == reflect.Slice && fieldIsStructSlice(fv) {
			b.WriteString(fmt.Sprintf("%s%s:\n", pad, key))
			for j := 0; j < fv.Len(); j++ {
				writeStruct(b, fv.Index(j), indent+1)
			}
			continue
		}
		b.WriteString(fmt.Sprintf("%s%-14s %s\n", pad, key+":", displayValue(fv)))
	}
}

func fieldIsStructSlice(v reflect.Value) bool {
	if v.Type().Elem().Kind() != reflect.Struct &&
		!(v.Type().Elem().Kind() == reflect.Ptr && v.Type().Elem().Elem().Kind() == reflect.Struct) {
		return false
	}
	return v.Len() > 0
}

func displayValue(v reflect.Value) string {
	for v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.Slice:
		parts := make([]string, 0, v.Len())
		for i := 0; i < v.Len(); i++ {
			parts = append(parts, displayValue(v.Index(i)))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case reflect.Struct:
		if s, ok := shortStruct(v); ok {
			return s
		}
	}
	return fmt.Sprintf("%v", v.Interface())
}

// shortStruct renders small value structs (tags, properties) inline.
func shortStruct(v reflect.Value) (string, bool) {
	t := v.Type()
	parts := []string{}
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		fv := v.Field(i)
		if isEmptyValue(fv) {
			continue
		}
		key := field.Name
		if tag := field.Tag.Get("json"); tag != "" && tag != "-" {
			key = strings.Split(tag, ",")[0]
		}
		val := fmt.Sprintf("%v", fv.Interface())
		if len(val) > 40 {
			return "", false
		}
		parts = append(parts, fmt.Sprintf("%s=%s", key, val))
	}
	if len(parts) == 0 {
		return "", false
	}
	sort.Strings(parts)
	return "{" + strings.Join(parts, " ") + "}", true
}

func isEmptyValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface:
		return v.IsNil()
	case reflect.Slice, reflect.Map:
		return v.Len() == 0
	case reflect.String:
		return v.Len() == 0
	case reflect.Struct:
		if t := v.Type(); t.String() == "time.Time" {
			return v.MethodByName("IsZero").Call(nil)[0].Bool()
		}
		return false
	default:
		return false
	}
}
