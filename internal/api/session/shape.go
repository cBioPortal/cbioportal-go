package session

import (
	"slices"

	"github.com/cbioportal/cbioportal-go/internal/api/sessionstore"
)

// Java's SessionServiceController and PublicVirtualStudiesController answer
// with a session read into a class for its type, written by the portal's
// CustomObjectMapper: null values are left out (NON_NULL), and the mixins
// hide the bookkeeping of the data classes (SessionDataMixin: owner,
// created, lastUpdated, version and, for virtual studies and custom data,
// users). Who saved a session and who else has it is never sent back. The
// views below are those classes.

// sessionView is a session as Java writes it: the class its type is read
// into (javaClass).
func sessionView(s *sessionstore.Session, class string) map[string]any {
	switch class {
	case "virtual_study":
		return map[string]any{"id": s.ID, "data": virtualStudyData(s.Data)}
	case "custom_data":
		return map[string]any{"id": s.ID, "data": pickNonNull(s.Data, customDataWritten)}
	case "settings":
		return map[string]any{"id": s.ID, "data": pageSettingsData(s.Data)}
	case "custom_gene_list":
		return map[string]any{"id": s.ID, "data": pickNonNull(s.Data, customGeneListFields)}
	}
	out := map[string]any{"id": s.ID, "data": withoutNulls(s.Data)}
	if s.Source != "" {
		out["source"] = s.Source
	}
	if s.Type != "" {
		out["type"] = s.Type
	}
	return out
}

// javaClass is the class Java reads a session of type typ into for
// GET /api/session/{type}/{id}.
func javaClass(typ string) string {
	switch typ {
	case "virtual_study", "custom_data", "settings", "custom_gene_list":
		return typ
	}
	return "session"
}

// sessionViews is sessionView of each session.
func sessionViews(list []*sessionstore.Session, class string) []map[string]any {
	out := make([]map[string]any, len(list))
	for i, s := range list {
		out[i] = sessionView(s, class)
	}
	return out
}

// The fields of Java's data classes that it writes (SessionDataMixin
// hides the rest). CustomGeneListData has no mixin, so a gene list is
// written with all its fields (customGeneListFields).
var (
	// VirtualStudyData, also for groups listed by groups/fetch.
	virtualStudyWritten = []string{"name", "description", "studies", "studyViewFilter", "origin", "typeOfCancerId", "pmid", "dynamic"}
	// CustomAttributeWithData.
	customDataWritten = []string{"displayName", "description", "datatype", "patientAttribute", "priority", "data", "origin"}
	// PageSettingsData's bookkeeping; its subclasses' own fields are written.
	pageSettingsHidden = []string{"owner", "created", "lastUpdated", "version", "users"}
)

// virtualStudyData is VirtualStudyData as Java writes it. Its origin is the
// studies' ids when none is stored, and its studyViewFilter is read into
// StudyViewFilter, which adds the filter's studies as uniqueStudyIds.
func virtualStudyData(data map[string]any) map[string]any {
	out := pickNonNull(data, virtualStudyWritten)
	if o, _ := out["origin"].([]any); len(o) == 0 {
		var ids []any
		studies, _ := data["studies"].([]any)
		for _, st := range studies {
			if m, ok := st.(map[string]any); ok && m["id"] != nil && !slices.Contains(ids, m["id"]) {
				ids = append(ids, m["id"])
			}
		}
		if ids != nil {
			out["origin"] = ids
		}
	}
	if f, ok := out["studyViewFilter"].(map[string]any); ok {
		out["studyViewFilter"] = studyViewFilterView(f)
	}
	return out
}

// studyViewFilterView is a stored study view filter as Java's
// StudyViewFilter writes it: without nulls, with uniqueStudyIds, the ids of
// studyIds and of sampleIdentifiers' studies.
func studyViewFilterView(f map[string]any) map[string]any {
	out := withoutNulls(f)
	var ids []any
	add := func(v any) {
		if s, ok := v.(string); ok && !slices.Contains(ids, v) {
			ids = append(ids, s)
		}
	}
	if si, _ := f["sampleIdentifiers"].([]any); len(si) > 0 {
		for _, x := range si {
			if m, ok := x.(map[string]any); ok {
				add(m["studyId"])
			}
		}
	}
	if st, _ := f["studyIds"].([]any); len(st) > 0 {
		for _, x := range st {
			add(x)
		}
	}
	if ids != nil {
		out["uniqueStudyIds"] = ids
	}
	return out
}

// pageSettingsData is PageSettingsData as Java writes it.
func pageSettingsData(data map[string]any) map[string]any {
	out := withoutNulls(data)
	for _, k := range pageSettingsHidden {
		delete(out, k)
	}
	return out
}

// pickNonNull copies the fields of m that are set and not null.
func pickNonNull(m map[string]any, fields []string) map[string]any {
	out := make(map[string]any, len(fields))
	for _, k := range fields {
		if v, ok := m[k]; ok && v != nil {
			out[k] = withoutNullsValue(v)
		}
	}
	return out
}

// withoutNulls copies m without null values, at any depth.
func withoutNulls(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if v != nil {
			out[k] = withoutNullsValue(v)
		}
	}
	return out
}

func withoutNullsValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		return withoutNulls(x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = withoutNullsValue(e)
		}
		return out
	}
	return v
}
