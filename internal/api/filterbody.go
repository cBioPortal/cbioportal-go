package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Request filters (the Java *Filter classes) as Jackson reads them and Bean
// Validation checks them. Jackson fails on properties the class does not
// declare and on values it cannot coerce: a string property takes any
// scalar, a list takes an array (not a single value), an Integer element a
// number (fractions truncated) or a string of one, an object element a JSON
// object of its own properties. Validation then reports one violation: a
// @Size on a present list, or the class's @AssertTrue on which of its id
// lists are present.

// filterKind is the Java type of a filter property.
type filterKind int

const (
	fString   filterKind = iota // String
	fStrings                    // List<String>
	fInts                       // List<Integer>
	fObjects                    // List<an object filter>
	fAnyArray                   // a list read leniently (not validated here)
)

type filterProp struct {
	name string
	// field is the Java field a violation names, when it is not name.
	field string
	kind  filterKind
	elem  *filterSchema // fObjects
	// sizeMin / sizeMax are the @Size bounds; sized is false without one.
	sizeMin, sizeMax int
	sized            bool
}

// filterSchema is one Java filter class.
type filterSchema struct {
	props []filterProp
	// either names the two properties exactly one of which must be present
	// (non-null), the class's @AssertTrue; eitherNonEmpty counts an empty
	// list as absent (CollectionUtils.isEmpty).
	either         [2]string
	eitherNonEmpty bool
	// assertName is the @AssertTrue's property name.
	assertName string
	// order lists the properties (and assertName) in the order Bean
	// Validation reports their violations.
	order []string
}

func sizedProp(name string, kind filterKind, min, max int) filterProp {
	return filterProp{name: name, kind: kind, sizeMin: min, sizeMax: max, sized: true}
}

var sampleMolecularIdentifierSchema = &filterSchema{props: []filterProp{
	{name: "sampleId", kind: fString}, {name: "molecularProfileId", kind: fString},
}}

// sampleListOrIDs is a profile filter with sampleIds XOR sampleListId plus
// extra properties; order is its violation order.
func sampleListOrIDs(order []string, extra ...filterProp) *filterSchema {
	props := append([]filterProp{sizedProp("sampleIds", fStrings, 1, maxPageSize), {name: "sampleListId", kind: fString}}, extra...)
	return &filterSchema{props: props, either: [2]string{"sampleListId", "sampleIds"},
		assertName: "eitherSampleListIdOrSampleIdsPresent", order: order}
}

// profilesOrIdentifiers is a multiple-study filter with molecularProfileIds
// XOR sampleMolecularIdentifiers plus extra properties.
func profilesOrIdentifiers(min int, order []string, extra ...filterProp) *filterSchema {
	ids := sizedProp("sampleMolecularIdentifiers", fObjects, min, maxPageSize)
	ids.elem = sampleMolecularIdentifierSchema
	props := append([]filterProp{ids, sizedProp("molecularProfileIds", fStrings, min, maxPageSize)}, extra...)
	return &filterSchema{props: props, either: [2]string{"molecularProfileIds", "sampleMolecularIdentifiers"},
		assertName: "eitherMolecularProfileIdsOrSampleMolecularIdentifiersPresent", order: order}
}

var (
	mutationFilterSchema = sampleListOrIDs([]string{"entrezGeneIds", "sampleIds", "eitherSampleListIdOrSampleIdsPresent"},
		sizedProp("entrezGeneIds", fInts, 1, maxPageSize))
	molecularDataFilterSchema = sampleListOrIDs([]string{"entrezGeneIds", "sampleIds", "eitherSampleListIdOrSampleIdsPresent"},
		sizedProp("entrezGeneIds", fInts, 1, maxPageSize))
	discreteCopyNumberFilterSchema = sampleListOrIDs([]string{"eitherSampleListIdOrSampleIdsPresent", "sampleIds", "entrezGeneIds"},
		sizedProp("entrezGeneIds", fInts, 1, 50000))
	genePanelDataFilterSchema    = sampleListOrIDs([]string{"sampleIds", "eitherSampleListIdOrSampleIdsPresent"})
	mutationSpectrumFilterSchema = sampleListOrIDs([]string{"sampleIds", "eitherSampleListIdOrSampleIdsPresent"})
	genericAssayFilterSchema     = sampleListOrIDs([]string{"sampleIds", "genericAssayStableIds", "eitherSampleListIdOrSampleIdsPresent"},
		filterProp{name: "genericAssayStableIds", field: "genericAssayStableId", kind: fStrings, sizeMin: 1, sizeMax: maxPageSize, sized: true})

	mutationMultipleStudyFilterSchema = profilesOrIdentifiers(1,
		[]string{"sampleMolecularIdentifiers", "entrezGeneIds", "eitherMolecularProfileIdsOrSampleMolecularIdentifiersPresent"},
		sizedProp("entrezGeneIds", fInts, 1, maxPageSize))
	molecularDataMultipleStudyFilterSchema = profilesOrIdentifiers(1,
		[]string{"sampleMolecularIdentifiers", "molecularProfileIds", "entrezGeneIds", "eitherMolecularProfileIdsOrSampleMolecularIdentifiersPresent"},
		sizedProp("entrezGeneIds", fInts, 1, maxPageSize))
	genePanelDataMultipleStudyFilterSchema = profilesOrIdentifiers(0,
		[]string{"eitherMolecularProfileIdsOrSampleMolecularIdentifiersPresent"})
	genericAssayDataMultipleStudyFilterSchema = profilesOrIdentifiers(1,
		[]string{"sampleMolecularIdentifiers", "molecularProfileIds", "genericAssayStableIds", "eitherMolecularProfileIdsOrSampleMolecularIdentifiersPresent"},
		sizedProp("genericAssayStableIds", fStrings, 1, maxPageSize))
	structuralVariantFilterSchema = func() *filterSchema {
		s := profilesOrIdentifiers(0, []string{"eitherMolecularProfileIdsOrSampleMolecularIdentifiersPresent"},
			sizedProp("entrezGeneIds", fInts, 0, maxPageSize), filterProp{name: "structuralVariantQueries", kind: fAnyArray})
		s.eitherNonEmpty = true
		return s
	}()
)

// readFilter reads body as Jackson reads the filter class: ok is false when
// it fails to (invalid JSON, a property the class does not declare, a value
// of the wrong type). The first Bean Validation violation is then the
// property and its message, or "".
func (fs *filterSchema) readFilter(body []byte) (ok bool, property, violation string) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return false, "", ""
	}
	obj, isObj := v.(map[string]any)
	if !isObj || !fs.read(obj) {
		return false, "", ""
	}
	property, violation = fs.violation(obj)
	return true, property, violation
}

// read checks obj's properties and their types.
func (fs *filterSchema) read(obj map[string]any) bool {
	for k, val := range obj {
		p := fs.prop(k)
		if p == nil || !p.read(val) {
			return false
		}
	}
	return true
}

func (fs *filterSchema) prop(name string) *filterProp {
	for i := range fs.props {
		if fs.props[i].name == name {
			return &fs.props[i]
		}
	}
	return nil
}

func (p *filterProp) read(val any) bool {
	if val == nil {
		return true
	}
	if p.kind == fString {
		return jsonScalar(val)
	}
	arr, ok := val.([]any)
	if !ok {
		return false
	}
	for _, el := range arr {
		if el == nil {
			continue
		}
		switch p.kind {
		case fStrings:
			if !jsonScalar(el) {
				return false
			}
		case fInts:
			if !javaIntegerElement(el) {
				return false
			}
		case fObjects:
			o, ok := el.(map[string]any)
			if !ok || !p.elem.read(o) {
				return false
			}
		}
	}
	return true
}

// jsonScalar reports whether v is a string, number or boolean, which a Java
// String property takes.
func jsonScalar(v any) bool {
	switch v.(type) {
	case string, json.Number, bool:
		return true
	}
	return false
}

// javaIntegerElement reports whether Jackson reads v as an Integer: a number
// in the int range (a fraction truncated) or a string of an int (spaces
// trimmed); an empty string reads as null.
func javaIntegerElement(v any) bool {
	switch x := v.(type) {
	case json.Number:
		f, err := strconv.ParseFloat(string(x), 64)
		return err == nil && f >= math.MinInt32 && f <= math.MaxInt32
	case string:
		if x == "" {
			return true
		}
		_, err := strconv.ParseInt(strings.TrimSpace(x), 10, 32)
		return err == nil
	}
	return false
}

// violation is the first Bean Validation violation of a read filter: its
// property and message.
func (fs *filterSchema) violation(obj map[string]any) (string, string) {
	present := func(name string) bool {
		v, ok := obj[name]
		if !ok || v == nil {
			return false
		}
		if arr, isArr := v.([]any); isArr && fs.eitherNonEmpty {
			return len(arr) > 0
		}
		return true
	}
	for _, name := range fs.order {
		if name == fs.assertName {
			if present(fs.either[0]) == present(fs.either[1]) {
				return name, "must be true"
			}
			continue
		}
		p := fs.prop(name)
		arr, ok := obj[name].([]any)
		if p == nil || !p.sized || !ok {
			continue
		}
		if len(arr) < p.sizeMin || len(arr) > p.sizeMax {
			if p.field != "" {
				name = p.field
			}
			return name, fmt.Sprintf("size must be between %d and %d", p.sizeMin, p.sizeMax)
		}
	}
	return "", ""
}
