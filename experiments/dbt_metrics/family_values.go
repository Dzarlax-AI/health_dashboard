package dbtmetrics

import (
	"encoding/json"
	"fmt"
	"path"
	"reflect"
	"sync"
)

type familySpec struct {
	GoType string `json:"go_type"`
	Owner  string `json:"owner"`
	Fields map[string]struct {
		Field string `json:"field"`
	} `json:"fields"`
}

var familySpecs = sync.OnceValues(func() (map[string]familySpec, error) {
	var doc struct {
		Models []struct {
			Config struct {
				Meta struct {
					Catalog struct {
						Families map[string]familySpec `json:"families"`
					} `json:"health_catalog"`
				}
			}
		}
	}
	if err := json.Unmarshal(metricCatalog, &doc); err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	if len(doc.Models) != 1 {
		return nil, fmt.Errorf("unexpected catalog model count")
	}
	return doc.Models[0].Config.Meta.Catalog.Families, nil
})

// ProjectFamilyValues maps an actual producer DTO to catalog metric IDs. It
// validates both the declared type and owning package; nullable values remain nil.
func ProjectFamilyValues(family string, record any) (map[string]any, error) {
	specs, err := familySpecs()
	if err != nil {
		return nil, err
	}
	spec, ok := specs[family]
	if !ok {
		return nil, fmt.Errorf("unknown metric family %q", family)
	}
	v := reflect.ValueOf(record)
	if !v.IsValid() {
		return nil, fmt.Errorf("nil record for %s", family)
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil, fmt.Errorf("nil record for %s", family)
		}
		v = v.Elem()
	}
	typ := v.Type()
	if v.Kind() != reflect.Struct || typ.Name() != spec.GoType || typ.PkgPath() != "health-receiver/"+path.Dir(spec.Owner) {
		return nil, fmt.Errorf("%s requires %s.%s, got %s", family, path.Dir(spec.Owner), spec.GoType, typ)
	}
	out := make(map[string]any, len(spec.Fields))
	for name, field := range spec.Fields {
		f := v.FieldByName(field.Field)
		if !f.IsValid() || !f.CanInterface() {
			return nil, fmt.Errorf("invalid field %s.%s: %s", family, name, field.Field)
		}
		if (f.Kind() == reflect.Pointer || f.Kind() == reflect.Slice || f.Kind() == reflect.Map || f.Kind() == reflect.Interface) && f.IsNil() {
			out[family+"."+name] = nil
		} else {
			out[family+"."+name] = f.Interface()
		}
	}
	return out, nil
}
