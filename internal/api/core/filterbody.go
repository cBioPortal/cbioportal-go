package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"slices"
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
	fObject                     // an object filter
	fEnum                       // a Java enum: one of values
)

type filterProp struct {
	name string
	// field is the Java field a violation names, when it is not name.
	field string
	kind  filterKind
	elem  *filterSchema // fObjects, fObject
	// values are the constants of an fEnum.
	values []string
	// sizeMin / sizeMax are the @Size bounds; sized is false without one.
	sizeMin, sizeMax int
	sized            bool
}

// filterSchema is one Java filter class.
type filterSchema struct {
	props []filterProp
	// listOf makes the body a List of this class instead (a JSON array of
	// its objects).
	listOf *filterSchema
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
	props := append([]filterProp{sizedProp("sampleIds", fStrings, 1, MaxPageSize), {name: "sampleListId", kind: fString}}, extra...)
	return &filterSchema{props: props, either: [2]string{"sampleListId", "sampleIds"},
		assertName: "eitherSampleListIdOrSampleIdsPresent", order: order}
}

// profilesOrIdentifiers is a multiple-study filter with molecularProfileIds
// XOR sampleMolecularIdentifiers plus extra properties.
func profilesOrIdentifiers(min int, order []string, extra ...filterProp) *filterSchema {
	ids := sizedProp("sampleMolecularIdentifiers", fObjects, min, MaxPageSize)
	ids.elem = sampleMolecularIdentifierSchema
	props := append([]filterProp{ids, sizedProp("molecularProfileIds", fStrings, min, MaxPageSize)}, extra...)
	return &filterSchema{props: props, either: [2]string{"molecularProfileIds", "sampleMolecularIdentifiers"},
		assertName: "eitherMolecularProfileIdsOrSampleMolecularIdentifiersPresent", order: order}
}

var (
	patientIdentifierSchema = &filterSchema{props: []filterProp{
		{name: "patientId", kind: fString}, {name: "studyId", kind: fString}}}
	// ClinicalEventRequest; ClinicalEventData's clinicalEventId (a Long) is
	// read as any scalar.
	clinicalEventRequestSchema = &filterSchema{props: []filterProp{
		{name: "eventType", kind: fString},
		{name: "attributes", kind: fObjects, elem: &filterSchema{props: []filterProp{
			{name: "clinicalEventId", kind: fString}, {name: "key", kind: fString}, {name: "value", kind: fString}}}},
	}}
	// ClinicalEventRequestIdentifier.
	clinicalEventRequestIdentifierSchema = &filterSchema{props: []filterProp{
		{name: "clinicalEventRequests", kind: fObjects, elem: clinicalEventRequestSchema},
		{name: "position", kind: fEnum, values: []string{"FIRST", "LAST"}},
	}}

	mutationFilterSchema = sampleListOrIDs([]string{"entrezGeneIds", "sampleIds", "eitherSampleListIdOrSampleIdsPresent"},
		sizedProp("entrezGeneIds", fInts, 1, MaxPageSize))
	molecularDataFilterSchema = sampleListOrIDs([]string{"entrezGeneIds", "sampleIds", "eitherSampleListIdOrSampleIdsPresent"},
		sizedProp("entrezGeneIds", fInts, 1, MaxPageSize))
	discreteCopyNumberFilterSchema = sampleListOrIDs([]string{"eitherSampleListIdOrSampleIdsPresent", "sampleIds", "entrezGeneIds"},
		sizedProp("entrezGeneIds", fInts, 1, 50000))
	genePanelDataFilterSchema    = sampleListOrIDs([]string{"sampleIds", "eitherSampleListIdOrSampleIdsPresent"})
	mutationSpectrumFilterSchema = sampleListOrIDs([]string{"sampleIds", "eitherSampleListIdOrSampleIdsPresent"})
	genericAssayFilterSchema     = sampleListOrIDs([]string{"sampleIds", "genericAssayStableIds", "eitherSampleListIdOrSampleIdsPresent"},
		filterProp{name: "genericAssayStableIds", field: "genericAssayStableId", kind: fStrings, sizeMin: 1, sizeMax: MaxPageSize, sized: true})

	mutationMultipleStudyFilterSchema = profilesOrIdentifiers(1,
		[]string{"sampleMolecularIdentifiers", "entrezGeneIds", "eitherMolecularProfileIdsOrSampleMolecularIdentifiersPresent"},
		sizedProp("entrezGeneIds", fInts, 1, MaxPageSize))
	molecularDataMultipleStudyFilterSchema = profilesOrIdentifiers(1,
		[]string{"sampleMolecularIdentifiers", "molecularProfileIds", "entrezGeneIds", "eitherMolecularProfileIdsOrSampleMolecularIdentifiersPresent"},
		sizedProp("entrezGeneIds", fInts, 1, MaxPageSize))
	genePanelDataMultipleStudyFilterSchema = profilesOrIdentifiers(0,
		[]string{"eitherMolecularProfileIdsOrSampleMolecularIdentifiersPresent"})
	genericAssayDataMultipleStudyFilterSchema = profilesOrIdentifiers(1,
		[]string{"sampleMolecularIdentifiers", "molecularProfileIds", "genericAssayStableIds", "eitherMolecularProfileIdsOrSampleMolecularIdentifiersPresent"},
		sizedProp("genericAssayStableIds", fStrings, 1, MaxPageSize))
	// ClinicalDataMultiStudyFilter (identifiers sized by
	// ClinicalDataController.CLINICAL_DATA_MAX_PAGE_SIZE).
	clinicalDataMultiStudyFilterSchema = func() *filterSchema {
		ids := sizedProp("identifiers", fObjects, 1, MaxPageSize)
		ids.elem = &filterSchema{props: []filterProp{{name: "entityId", kind: fString}, {name: "studyId", kind: fString}}}
		return &filterSchema{props: []filterProp{ids, sizedProp("attributeIds", fStrings, 1, MaxPageSize)},
			order: []string{"identifiers", "attributeIds"}}
	}()
	// ClinicalDataSingleStudyFilter (ids sized by
	// ClinicalDataController.CLINICAL_DATA_MAX_PAGE_SIZE).
	clinicalDataSingleStudyFilterSchema = &filterSchema{
		props: []filterProp{sizedProp("ids", fStrings, 1, MaxPageSize), sizedProp("attributeIds", fStrings, 1, MaxPageSize)},
		order: []string{"ids", "attributeIds"}}
	// PatientFilter.
	patientFilterSchema = func() *filterSchema {
		ids := sizedProp("patientIdentifiers", fObjects, 1, MaxPageSize)
		ids.elem = patientIdentifierSchema
		return &filterSchema{props: []filterProp{ids, sizedProp("uniquePatientKeys", fStrings, 1, MaxPageSize)},
			either:     [2]string{"patientIdentifiers", "uniquePatientKeys"},
			assertName: "eitherPatientIdentifiersOrUniquePatientKeysPresent",
			order:      []string{"patientIdentifiers", "uniquePatientKeys", "eitherPatientIdentifiersOrUniquePatientKeysPresent"}}
	}()
	// MolecularProfileFilter.
	molecularProfileFilterSchema = &filterSchema{
		props:      []filterProp{sizedProp("studyIds", fStrings, 1, MaxPageSize), sizedProp("molecularProfileIds", fStrings, 1, MaxPageSize)},
		either:     [2]string{"studyIds", "molecularProfileIds"},
		assertName: "eitherStudyIdsOrMolecularProfileIdsPresent",
		order:      []string{"studyIds", "molecularProfileIds", "eitherStudyIdsOrMolecularProfileIdsPresent"}}
	// List<SampleIdentifier>, the copy-number-segments/fetch body.
	sampleIdentifierListSchema = &filterSchema{listOf: &filterSchema{props: []filterProp{
		{name: "sampleId", kind: fString}, {name: "studyId", kind: fString}}}}
	// SurvivalRequest; its violations are left to the handler.
	survivalRequestSchema = &filterSchema{props: []filterProp{
		{name: "patientIdentifiers", kind: fObjects, elem: patientIdentifierSchema},
		{name: "attributeIdPrefix", kind: fString},
		{name: "startEventRequestIdentifier", kind: fObject, elem: clinicalEventRequestIdentifierSchema},
		{name: "endEventRequestIdentifier", kind: fObject, elem: clinicalEventRequestIdentifierSchema},
		{name: "censoredEventRequestIdentifier", kind: fObject, elem: clinicalEventRequestIdentifierSchema},
	}}
	// ClinicalEventAttributeRequest; its violations are left to the handler.
	clinicalEventAttributeRequestSchema = &filterSchema{props: []filterProp{
		{name: "patientIdentifiers", kind: fObjects, elem: patientIdentifierSchema},
		{name: "clinicalEventRequests", kind: fObjects, elem: clinicalEventRequestSchema},
	}}
	structuralVariantFilterSchema = func() *filterSchema {
		s := profilesOrIdentifiers(0, []string{"eitherMolecularProfileIdsOrSampleMolecularIdentifiersPresent"},
			sizedProp("entrezGeneIds", fInts, 0, MaxPageSize), filterProp{name: "structuralVariantQueries", kind: fAnyArray})
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
	if fs.listOf != nil {
		arr, isArr := v.([]any)
		return isArr && (&filterProp{kind: fObjects, elem: fs.listOf}).read(arr), "", ""
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
	switch p.kind {
	case fString:
		return jsonScalar(val)
	case fEnum:
		str, ok := val.(string)
		return ok && slices.Contains(p.values, str)
	case fObject:
		o, ok := val.(map[string]any)
		return ok && p.elem.read(o)
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
