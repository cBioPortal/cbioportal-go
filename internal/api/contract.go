package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// javaContract applies the request handling the Java controllers get from
// Spring around the core resource endpoints, so their handlers only produce
// data:
//
//   - request parameters convert like Spring's: an unknown enum constant
//     (projection, sortBy, direction, clinicalDataType, geneIdType) or a
//     non-integer pageSize / pageNumber is a 400 type mismatch, and the
//     @Min / @Max paging bounds and @Size(min = 1) list bodies are 400
//     constraint violations;
//   - an unknown study, patient, sample, sample list or molecular profile in
//     the path is a 404 with the Java message, as are a handler's 404s;
//   - the response carries the fields of the requested projection only, and
//     DETAILED adds the nested study where the Java model has one.
func (s *State) javaContract(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rt, vals := matchContract(r.Method, r.URL.Path)
		if rt == nil {
			next.ServeHTTP(w, r)
			return
		}
		q := r.URL.Query()
		if rt.filter != nil && r.Method == http.MethodPost {
			msg, done := rt.checkFilter(w, r, q)
			if done {
				return
			}
			if msg != "" {
				writeError(w, http.StatusBadRequest, msg)
				return
			}
		} else if msg := checkParams(rt.params, q); msg != "" {
			readBody(r, nil)
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		if rt.body != nil && r.Method == http.MethodPost {
			raw, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(raw))
			if msg := rt.body(raw); msg != "" {
				writeError(w, http.StatusBadRequest, msg)
				return
			}
		}
		for _, kind := range rt.exists {
			if msg := s.missing(kind, vals); msg != "" {
				readBody(r, nil)
				writeError(w, http.StatusNotFound, msg)
				return
			}
		}
		projection := rt.fixed
		if projection == "" {
			projection = strings.TrimSpace(q.Get("projection"))
			if projection == "" {
				projection = "SUMMARY"
			}
		}
		keep, shaped := rt.shape[projection]
		nest := ""
		if projection == "DETAILED" {
			nest = rt.nest
		}
		if !shaped && nest == "" && rt.notFound == nil {
			next.ServeHTTP(w, r)
			return
		}
		// A successful array is reshaped element by element as it is written;
		// anything else is kept whole.
		var bw *bufio.Writer
		var rs *reshaper
		n := 0
		rec := newElementWriter(func(h http.Header) bool {
			if !shaped && nest == "" {
				return false
			}
			rs = s.newReshaper(keep, shaped, nest)
			maps.Copy(w.Header(), h)
			w.Header().Del("Content-Length")
			w.WriteHeader(http.StatusOK)
			bw = bufio.NewWriterSize(w, 64<<10)
			bw.WriteByte('[')
			return true
		}, func(el []byte) error {
			if n > 0 {
				bw.WriteByte(',')
			}
			n++
			if !rs.object(bw, el) {
				bw.Write(el)
			}
			_, err := bw.Write(nil) // the error of an earlier write, if any
			return err
		})
		next.ServeHTTP(rec, r)
		if rec.finish() {
			bw.WriteString("]\n")
			bw.Flush()
			return
		}
		maps.Copy(w.Header(), rec.header)
		if rec.status == http.StatusNotFound && rt.notFound != nil {
			w.Header().Del("X-Content-Type-Options")
			writeError(w, http.StatusNotFound, rt.notFound(vals))
			return
		}
		body := rec.raw.Bytes()
		if rec.status == http.StatusOK && (shaped || nest != "") {
			if out, ok := s.reshape(body, keep, shaped, nest); ok {
				body = out
				w.Header().Del("Content-Length")
			}
		}
		w.WriteHeader(rec.status)
		w.Write(body)
	})
}

// ---------- request parameters ----------

const (
	legacyProjection = "org.cbioportal.legacy.web.parameter.Projection"
	projectionType   = "org.cbioportal.shared.enums.ProjectionType"
	sortByPackage    = "org.cbioportal.legacy.web.parameter.sort."
	maxPageSize      = 10000000
)

// param is one converted request parameter: a Java enum (class + constants)
// or an Integer with optional bounds; a required one must be present.
type param struct {
	name           string
	class          string
	values         []string
	min, max       int
	hasMin, hasMax bool
	required       bool
	text           bool // a String parameter: only its presence is checked
}

func projectionParamOf(class string) param {
	return param{name: "projection", class: class, values: []string{"ID", "SUMMARY", "DETAILED", "META"}}
}

func enumParam(name, class string, values ...string) param {
	return param{name: name, class: class, values: values}
}

func sortBy(enum string, values ...string) param {
	return enumParam("sortBy", sortByPackage+enum, values...)
}

var (
	pageSizeParam   = param{name: "pageSize", min: 1, max: maxPageSize, hasMin: true, hasMax: true}
	pageNumberParam = param{name: "pageNumber", min: 0, hasMin: true}
	directionParam  = enumParam("direction", "org.cbioportal.legacy.web.parameter.Direction", "ASC", "DESC")
)

// requiredText is a required String request parameter.
func requiredText(name string) param { return param{name: name, required: true, text: true} }

// paged is the projection / pageSize / pageNumber / sortBy / direction
// parameter list of a Java list endpoint.
func paged(projClass string, sort param) []param {
	return pagedMax(projClass, sort, maxPageSize)
}

// pagedMax is paged with an endpoint's own @Max page size.
func pagedMax(projClass string, sort param, maxSize int) []param {
	size := pageSizeParam
	size.max = maxSize
	return []param{projectionParamOf(projClass), size, pageNumberParam, sort, directionParam}
}

// checkParams converts the parameters in declaration order (the first
// failure is the TypeMismatchException), then checks their bounds (a
// ConstraintViolationException). Absent or blank values take the default.
func checkParams(params []param, q map[string][]string) string {
	return checkParamsAround(params, q, len(params), nil)
}

// checkParamsAround is checkParams with the request body read after the
// first at parameters, where the controller declares it: body (when
// non-nil) returns its 400 message or "".
func checkParamsAround(params []param, q map[string][]string, at int, body func() string) string {
	type bound struct {
		p param
		v int
	}
	var bounds []bound
	for i, p := range params {
		if i == at && body != nil {
			if msg := body(); msg != "" {
				return msg
			}
		}
		vs := q[p.name]
		// A String parameter is present even when blank; other types read
		// blank as absent.
		if len(vs) == 0 || (!p.text && strings.TrimSpace(vs[0]) == "") {
			if p.required {
				return "Request parameter is missing: " + p.name
			}
			continue
		}
		v := strings.TrimSpace(vs[0])
		if p.text {
			continue
		}
		if p.class != "" {
			ok := false
			for _, c := range p.values {
				if c == v {
					ok = true
					break
				}
			}
			if !ok {
				return msgEnumMismatch(p.class, v)
			}
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return msgIntMismatch(v)
		}
		bounds = append(bounds, bound{p, n})
	}
	if at >= len(params) && body != nil {
		if msg := body(); msg != "" {
			return msg
		}
	}
	for _, b := range bounds {
		if b.p.hasMin && b.v < b.p.min {
			return fmt.Sprintf("%s must be greater than or equal to %d", b.p.name, b.p.min)
		}
		if b.p.hasMax && b.v > b.p.max {
			return fmt.Sprintf("%s must be less than or equal to %d", b.p.name, b.p.max)
		}
	}
	return ""
}

// listBody validates a JSON-array request body annotated @Size(min = 1):
// an empty array is a violation named after the controller parameter.
// Bodies that are not arrays are left to the handler (readBody).
func listBody(name string) func([]byte) string {
	return func(raw []byte) string {
		if emptyJSONArray(raw) && json.Valid(raw) {
			return msgListSize(name)
		}
		return ""
	}
}

// emptyJSONArray reports whether b, ignoring JSON whitespace, is [].
func emptyJSONArray(b []byte) bool {
	i := skipSpace(b, 0)
	if i >= len(b) || b[i] != '[' {
		return false
	}
	i = skipSpace(b, i+1)
	return i < len(b) && b[i] == ']' && skipSpace(b, i+1) == len(b)
}

// sampleFilterBody validates SampleFilter: each present list holds at least
// one item, and exactly one of them — or all three — is present
// (isOnlyOneTypeOfFilterPresent XORs the three).
func sampleFilterBody(raw []byte) string {
	var f map[string]json.RawMessage
	if json.Unmarshal(raw, &f) != nil {
		return ""
	}
	present := 0
	for _, k := range []string{"sampleIdentifiers", "sampleListIds", "uniqueSampleKeys"} {
		v, ok := f[k]
		if !ok || string(v) == "null" {
			continue
		}
		present++
		if emptyJSONArray(v) {
			return msgListSize(k)
		}
	}
	if present%2 == 0 {
		return "onlyOneTypeOfFilterPresent must be true"
	}
	return ""
}

// ---------- routes ----------

// contractRoute describes one Java endpoint.
type contractRoute struct {
	method, pattern string
	params          []param
	// body validates a POST body; it returns the 400 message or "".
	body func([]byte) string
	// exists lists the path entities checked before the handler runs:
	// study, patient, sample, sampleList, profile.
	exists []string
	// shape maps a projection to the fields its objects keep; projections
	// absent from the map keep every field.
	shape map[string][]string
	// fixed is the projection of an endpoint without a projection parameter.
	fixed string
	// nest names the field the DETAILED projection adds the legacy study under.
	nest string
	// notFound turns the handler's 404 into Java's message.
	notFound func(vals map[string]string) string
	// filter is the Java filter class of a POST body, declared after the
	// first bodyAt params.
	filter *filterSchema
	bodyAt int
	// intercepted names the controller parameter of a filter that
	// InvolvedCancerStudyExtractorInterceptor reads first: a body it cannot
	// read ends the request with an empty 200, and violations name the
	// parameter.
	intercepted string
	// unvalidated leaves the filter's violations to the handler, which
	// answers them itself (GenericAssayDataController).
	unvalidated bool
}

// checkFilter checks the parameters and filter body of a POST with a
// filter: msg is the 400 message, done reports an answered request.
func (rt *contractRoute) checkFilter(w http.ResponseWriter, r *http.Request, q map[string][]string) (msg string, done bool) {
	raw, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(raw))
	ok, prop, violation := rt.filter.readFilter(raw)
	if rt.intercepted != "" {
		if !ok {
			w.WriteHeader(http.StatusOK)
			return "", true
		}
		// The filter is validated with the method's parameters, after
		// they are converted.
		return checkParamsAround(rt.params, q, len(rt.params), func() string {
			if violation != "" && !rt.unvalidated {
				return rt.intercepted + " " + violation
			}
			return ""
		}), false
	}
	return checkParamsAround(rt.params, q, rt.bodyAt, func() string {
		switch {
		case !ok:
			return msgBadJSON
		case violation != "":
			return prop + " " + violation
		}
		return ""
	}), false
}

var (
	sampleFieldsID      = []string{"sampleId", "patientId", "studyId", "uniqueSampleKey", "uniquePatientKey"}
	sampleShape         = map[string][]string{"ID": sampleFieldsID, "SUMMARY": append(append([]string{}, sampleFieldsID...), "sampleType")}
	patientFields       = []string{"patientId", "studyId", "uniquePatientKey"}
	patientShape        = map[string][]string{"ID": patientFields, "SUMMARY": patientFields, "DETAILED": patientFields}
	sampleListShape     = map[string][]string{"ID": {"sampleListId"}, "SUMMARY": {"sampleListId", "studyId", "category", "name", "description"}}
	clinicalAttrShape   = map[string][]string{"ID": {"clinicalAttributeId", "studyId", "patientAttribute", "displayName"}}
	geneFields          = []string{"entrezGeneId", "hugoGeneSymbol", "type"}
	geneShape           = map[string][]string{"ID": {"entrezGeneId", "hugoGeneSymbol"}, "SUMMARY": geneFields, "DETAILED": geneFields}
	genePanelShape      = map[string][]string{"ID": {"genePanelId"}, "SUMMARY": {"genePanelId", "description"}}
	cancerTypeShape     = map[string][]string{"ID": {"cancerTypeId"}}
	molecularProfileIDs = []string{"molecularProfileId", "studyId"}
	molecularProfileSum = []string{"molecularProfileId", "studyId", "molecularAlterationType", "datatype", "name",
		"description", "showProfileInAnalysisTab", "pivotThreshold", "sortOrder", "genericAssayType", "patientLevel"}
	molecularProfileShape = map[string][]string{"ID": molecularProfileIDs, "SUMMARY": molecularProfileSum, "DETAILED": molecularProfileSum}
)

var (
	studySort            = sortBy("StudySortBy", "studyId", "cancerTypeId", "name", "description", "publicStudy", "pmid", "citation", "groups", "status", "importDate")
	cancerTypeSort       = sortBy("CancerTypeSortBy", "cancerTypeId", "name", "dedicatedColor", "shortName", "parent")
	sampleSort           = sortBy("SampleSortBy", "sampleId", "sampleType")
	patientSort          = sortBy("PatientSortBy", "patientId")
	sampleListSort       = sortBy("SampleListSortBy", "sampleListId", "category", "studyId", "name", "description")
	clinicalAttrSort     = sortBy("ClinicalAttributeSortBy", "clinicalAttributeId", "displayName", "description", "datatype", "patientAttribute", "priority", "studyId")
	clinicalDataSort     = sortBy("ClinicalDataSortBy", "clinicalAttributeId", "value")
	molecularProfileSort = sortBy("MolecularProfileSortBy", "molecularProfileId", "molecularAlterationType", "datatype", "name", "description", "showProfileInAnalysisTab")
	geneSort             = sortBy("GeneSortBy", "entrezGeneId", "hugoGeneSymbol", "type", "cytoband", "length")
	genePanelSort        = sortBy("GenePanelSortBy", "genePanelId", "description")
	clinicalEventSort    = sortBy("ClinicalEventSortBy", "eventType", "startNumberOfDaysSinceDiagnosis", "endNumberOfDaysSinceDiagnosis")
	mutationSort         = sortBy("MutationSortBy", "entrezGeneId", "center", "mutationStatus", "validationStatus",
		"tumorAltCount", "tumorRefCount", "normalAltCount", "normalRefCount", "aminoAcidChange", "startPosition",
		"endPosition", "referenceAllele", "variantAllele", "proteinChange", "mutationType", "ncbiBuild", "variantType",
		"refseqMrnaId", "proteinPosStart", "proteinPosEnd", "keyword")
	resourceDataSortParam       = sortBy("ResourceDataSortBy", "ResourceId", "url")
	resourceDefinitionSortParam = sortBy("ResourceDefinitionSortBy", "resourceId", "displayName", "description", "resourceType",
		"priority", "openByDefault", "studyId", "customMetaData")
	copyNumberSegmentSort       = sortBy("CopyNumberSegmentSortBy", "chromosome", "start", "end", "numberOfProbes", "segmentMean")
	smgSort                     = sortBy("SignificantlyMutatedGeneSortBy", "entrezGeneId", "hugoGeneSymbol", "rank", "numberOfMutations", "pValue", "qValue")
	scnrSort                    = sortBy("SignificantCopyNumberRegionSortBy", "chromosome", "cytoband", "widePeakStart", "widePeakEnd", "qValue", "amp")
	resourceIDParam             = param{name: "resourceId", text: true}
	sampleListIDParam           = requiredText("sampleListId")
	discreteCopyNumberEventType = enumParam("discreteCopyNumberEventType", "org.cbioportal.legacy.web.parameter.DiscreteCopyNumberEventType",
		"HOMDEL_AND_AMP", "HOMDEL", "AMP", "GAIN", "HETLOSS", "DIPLOID", "ALL")
	clinicalDataType = enumParam("clinicalDataType", "org.cbioportal.legacy.web.parameter.ClinicalDataType", "SAMPLE", "PATIENT")
	geneIDType       = enumParam("geneIdType", "org.cbioportal.legacy.web.parameter.GeneIdType", "ENTREZ_GENE_ID", "HUGO_GENE_SYMBOL")
)

func pathValue(key string, msg func(string) string) func(map[string]string) string {
	return func(v map[string]string) string { return msg(v[key]) }
}

var contractRoutes = []contractRoute{
	// Studies (ColumnStoreStudyController; /studies/fetch is legacy).
	{method: "GET", pattern: "/api/studies", params: []param{projectionParamOf(projectionType), studySort, pageSizeParam, pageNumberParam, directionParam}},
	{method: "GET", pattern: "/api/studies/{studyId}", exists: []string{"study"}},
	{method: "POST", pattern: "/api/studies/fetch", params: []param{projectionParamOf(legacyProjection)}, body: listBody("studyIds")},

	// Cancer types.
	{method: "GET", pattern: "/api/cancer-types", params: paged(legacyProjection, cancerTypeSort), shape: cancerTypeShape},
	{method: "GET", pattern: "/api/cancer-types/{cancerTypeId}", fixed: "DETAILED", notFound: pathValue("cancerTypeId", msgCancerTypeNotFound)},

	// Samples (ColumnStoreSampleController).
	{method: "GET", pattern: "/api/samples", params: []param{projectionParamOf(projectionType), pageSizeParam, pageNumberParam, sampleSort, directionParam}, shape: sampleShape},
	{method: "GET", pattern: "/api/studies/{studyId}/samples", params: paged(projectionType, sampleSort), exists: []string{"study"}, shape: sampleShape},
	{method: "GET", pattern: "/api/studies/{studyId}/samples/{sampleId}", exists: []string{"study", "sample"}, fixed: "DETAILED"},
	{method: "GET", pattern: "/api/studies/{studyId}/patients/{patientId}/samples", params: paged(projectionType, sampleSort), exists: []string{"study", "patient"}, shape: sampleShape},
	{method: "POST", pattern: "/api/samples/fetch", params: []param{projectionParamOf(projectionType)}, body: sampleFilterBody, shape: sampleShape},
	// The study view's filtered samples carry the ID fields only (the SampleDTO's
	// other fields are not selected, so null and not serialized).
	{method: "POST", pattern: "/api/filtered-samples/fetch", fixed: "ID", shape: sampleShape},

	// Patients.
	{method: "GET", pattern: "/api/patients", params: paged(legacyProjection, patientSort), shape: patientShape, nest: "cancerStudy"},
	{method: "GET", pattern: "/api/studies/{studyId}/patients", params: paged(legacyProjection, patientSort), exists: []string{"study"}, shape: patientShape, nest: "cancerStudy"},
	{method: "GET", pattern: "/api/studies/{studyId}/patients/{patientId}", exists: []string{"study", "patient"}, fixed: "DETAILED", shape: patientShape, nest: "cancerStudy"},
	{method: "POST", pattern: "/api/patients/fetch", params: []param{projectionParamOf(legacyProjection)}, shape: patientShape, nest: "cancerStudy"},

	// Sample lists.
	{method: "GET", pattern: "/api/sample-lists", params: paged(legacyProjection, sampleListSort), shape: sampleListShape},
	{method: "GET", pattern: "/api/studies/{studyId}/sample-lists", params: paged(legacyProjection, sampleListSort), exists: []string{"study"}, shape: sampleListShape},
	{method: "GET", pattern: "/api/sample-lists/{sampleListId}", fixed: "DETAILED", notFound: pathValue("sampleListId", msgSampleListNotFound)},
	{method: "GET", pattern: "/api/sample-lists/{sampleListId}/sample-ids", exists: []string{"sampleList"}},
	{method: "POST", pattern: "/api/sample-lists/fetch", params: []param{projectionParamOf(legacyProjection)}, body: listBody("sampleListIds"), shape: sampleListShape},

	// Clinical attributes.
	{method: "GET", pattern: "/api/clinical-attributes", params: paged(legacyProjection, clinicalAttrSort), shape: clinicalAttrShape},
	{method: "GET", pattern: "/api/studies/{studyId}/clinical-attributes", params: paged(legacyProjection, clinicalAttrSort), exists: []string{"study"}, shape: clinicalAttrShape},
	{method: "GET", pattern: "/api/studies/{studyId}/clinical-attributes/{clinicalAttributeId}", exists: []string{"study"}, fixed: "DETAILED",
		notFound: func(v map[string]string) string {
			return msgClinicalAttributeNotFound(v["studyId"], v["clinicalAttributeId"])
		}},
	{method: "POST", pattern: "/api/clinical-attributes/fetch", params: []param{projectionParamOf(legacyProjection)}, body: listBody("studyIds"), shape: clinicalAttrShape},

	// Clinical data (projections applied by writeClinical).
	{method: "GET", pattern: "/api/studies/{studyId}/samples/{sampleId}/clinical-data", params: paged(legacyProjection, clinicalDataSort), exists: []string{"study", "sample"}},
	{method: "GET", pattern: "/api/studies/{studyId}/patients/{patientId}/clinical-data", params: paged(legacyProjection, clinicalDataSort), exists: []string{"study", "patient"}},
	{method: "GET", pattern: "/api/studies/{studyId}/clinical-data", params: append([]param{clinicalDataType}, paged(legacyProjection, clinicalDataSort)...), exists: []string{"study"}},
	{method: "POST", pattern: "/api/studies/{studyId}/clinical-data/fetch", params: []param{clinicalDataType, projectionParamOf(legacyProjection)}, exists: []string{"study"}},
	{method: "POST", pattern: "/api/clinical-data/fetch", params: []param{clinicalDataType, projectionParamOf(legacyProjection)}},

	// Clinical events (the study's list has its own page size bound).
	{method: "GET", pattern: "/api/studies/{studyId}/patients/{patientId}/clinical-events", params: paged(legacyProjection, clinicalEventSort)},
	{method: "GET", pattern: "/api/studies/{studyId}/clinical-events", params: pagedMax(legacyProjection, clinicalEventSort, 100000)},

	// Resources, copy-number segments, significance lists.
	{method: "GET", pattern: "/api/studies/{studyId}/samples/{sampleId}/resource-data", params: append([]param{resourceIDParam}, paged(legacyProjection, resourceDataSortParam)...)},
	{method: "GET", pattern: "/api/studies/{studyId}/patients/{patientId}/resource-data", params: append([]param{resourceIDParam}, paged(legacyProjection, resourceDataSortParam)...)},
	{method: "GET", pattern: "/api/studies/{studyId}/resource-data", params: append([]param{resourceIDParam}, paged(legacyProjection, resourceDataSortParam)...)},
	{method: "GET", pattern: "/api/studies/{studyId}/resource-data-all", params: append([]param{resourceIDParam}, paged(legacyProjection, resourceDataSortParam)...)},
	{method: "GET", pattern: "/api/studies/{studyId}/resource-definitions", params: paged(legacyProjection, resourceDefinitionSortParam)},
	{method: "GET", pattern: "/api/studies/{studyId}/samples/{sampleId}/copy-number-segments",
		params: append([]param{{name: "chromosome", text: true}}, pagedMax(legacyProjection, copyNumberSegmentSort, 20000)...)},
	{method: "GET", pattern: "/api/studies/{studyId}/significantly-mutated-genes", params: paged(legacyProjection, smgSort)},
	{method: "GET", pattern: "/api/studies/{studyId}/significant-copy-number-regions", params: paged(legacyProjection, scnrSort)},

	// Molecular profiles.
	{method: "GET", pattern: "/api/molecular-profiles", params: paged(legacyProjection, molecularProfileSort), shape: molecularProfileShape, nest: "study"},
	{method: "GET", pattern: "/api/molecular-profiles/{molecularProfileId}", exists: []string{"profile"}, fixed: "DETAILED", shape: molecularProfileShape, nest: "study"},
	{method: "GET", pattern: "/api/studies/{studyId}/molecular-profiles", params: paged(legacyProjection, molecularProfileSort), exists: []string{"study"}, shape: molecularProfileShape, nest: "study"},
	{method: "POST", pattern: "/api/molecular-profiles/fetch", params: []param{projectionParamOf(legacyProjection)}, shape: molecularProfileShape, nest: "study"},
	{method: "GET", pattern: "/api/molecular-profiles/{molecularProfileId}/mutations", exists: []string{"profile"},
		params: append([]param{sampleListIDParam, {name: "entrezGeneId", required: true}},
			paged(legacyProjection, mutationSort)...)},
	{method: "POST", pattern: "/api/molecular-profiles/{molecularProfileId}/mutations/fetch", exists: []string{"profile"},
		params: paged(legacyProjection, mutationSort), filter: mutationFilterSchema},
	{method: "POST", pattern: "/api/mutations/fetch", params: paged(legacyProjection, mutationSort),
		filter: mutationMultipleStudyFilterSchema, intercepted: "interceptedMutationMultipleStudyFilter"},
	{method: "GET", pattern: "/api/molecular-profiles/{molecularProfileId}/molecular-data", exists: []string{"profile"},
		params: []param{sampleListIDParam, {name: "entrezGeneId", required: true}, projectionParamOf(legacyProjection)}},
	{method: "POST", pattern: "/api/molecular-profiles/{molecularProfileId}/molecular-data/fetch", exists: []string{"profile"},
		params: []param{projectionParamOf(legacyProjection)}, filter: molecularDataFilterSchema},
	{method: "POST", pattern: "/api/molecular-data/fetch", params: []param{projectionParamOf(legacyProjection)},
		filter: molecularDataMultipleStudyFilterSchema, intercepted: "interceptedMolecularDataMultipleStudyFilter"},
	{method: "GET", pattern: "/api/molecular-profiles/{molecularProfileId}/discrete-copy-number", exists: []string{"profile"},
		params: []param{sampleListIDParam, discreteCopyNumberEventType, projectionParamOf(legacyProjection)}},
	{method: "POST", pattern: "/api/molecular-profiles/{molecularProfileId}/discrete-copy-number/fetch", exists: []string{"profile"},
		params: []param{discreteCopyNumberEventType, projectionParamOf(legacyProjection)}, bodyAt: 1, filter: discreteCopyNumberFilterSchema},
	{method: "POST", pattern: "/api/molecular-profiles/{molecularProfileId}/discrete-copy-number-counts/fetch", exists: []string{"profile"}},
	{method: "POST", pattern: "/api/molecular-profiles/{molecularProfileId}/gene-panel-data/fetch", exists: []string{"profile"},
		filter: genePanelDataFilterSchema},
	{method: "POST", pattern: "/api/gene-panel-data/fetch", filter: genePanelDataMultipleStudyFilterSchema,
		intercepted: "interceptedGenePanelDataMultipleStudyFilter"},
	{method: "POST", pattern: "/api/structural-variant/fetch", filter: structuralVariantFilterSchema,
		intercepted: "interceptedStructuralVariantFilter"},
	{method: "POST", pattern: "/api/molecular-profiles/{molecularProfileId}/mrna-percentile/fetch", exists: []string{"profile"},
		params: []param{requiredText("sampleId")}},
	{method: "POST", pattern: "/api/molecular-profiles/co-expressions/fetch",
		params: []param{requiredText("molecularProfileIdA"), requiredText("molecularProfileIdB")}},
	{method: "POST", pattern: "/api/clinical-data-density-plot/fetch", params: []param{requiredText("xAxisAttributeId"),
		{name: "xAxisBinCount"}, requiredText("yAxisAttributeId"), {name: "yAxisBinCount"}}},
	{method: "POST", pattern: "/api/clinical-data-violin-plots/fetch",
		params: []param{requiredText("categoricalAttributeId"), requiredText("numericalAttributeId")}},
	{method: "POST", pattern: "/api/geneset-hierarchy/fetch", params: []param{requiredText("geneticProfileId"), {name: "percentile"}}},
	{method: "POST", pattern: "/api/genesets/{genesetId}/expression-correlation/fetch", params: []param{requiredText("geneticProfileId")}},
	{method: "POST", pattern: "/api/molecular-profiles/{molecularProfileId}/mutation-spectrums/fetch", exists: []string{"profile"},
		filter: mutationSpectrumFilterSchema},
	{method: "POST", pattern: "/api/molecular-profiles/{molecularProfileId}/variant-counts/fetch", exists: []string{"profile"}},
	{method: "GET", pattern: "/api/generic-assay-data/{molecularProfileId}/generic-assay/{genericAssayStableId}", exists: []string{"profile"}},
	{method: "POST", pattern: "/api/generic_assay_data/{molecularProfileId}/fetch", exists: []string{"profile"},
		params: []param{projectionParamOf(legacyProjection)}, filter: genericAssayFilterSchema},
	{method: "POST", pattern: "/api/generic_assay_data/fetch", params: []param{projectionParamOf(legacyProjection)},
		filter: genericAssayDataMultipleStudyFilterSchema, intercepted: "interceptedGenericAssayDataMultipleStudyFilter", unvalidated: true},

	// Genes.
	{method: "GET", pattern: "/api/genes", params: paged(legacyProjection, geneSort), shape: geneShape},
	{method: "POST", pattern: "/api/genes/fetch", params: []param{geneIDType, projectionParamOf(legacyProjection)}, body: listBody("geneIds"), shape: geneShape},

	// Gene panels.
	{method: "GET", pattern: "/api/gene-panels", params: paged(legacyProjection, genePanelSort), shape: genePanelShape},
	{method: "GET", pattern: "/api/gene-panels/{genePanelId}", fixed: "DETAILED", notFound: pathValue("genePanelId", msgGenePanelNotFound)},
	{method: "POST", pattern: "/api/gene-panels/fetch", params: []param{projectionParamOf(legacyProjection)}, body: listBody("genePanelIds"), shape: genePanelShape},
}

// matchContract finds the route for a request and its path values.
func matchContract(method, path string) (*contractRoute, map[string]string) {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	for i := range contractRoutes {
		rt := &contractRoutes[i]
		if rt.method != method {
			continue
		}
		pat := strings.Split(strings.Trim(rt.pattern, "/"), "/")
		if len(pat) != len(segs) {
			continue
		}
		vals := map[string]string{}
		ok := true
		for j, p := range pat {
			if strings.HasPrefix(p, "{") {
				vals[strings.Trim(p, "{}")] = segs[j]
			} else if p != segs[j] {
				ok = false
				break
			}
		}
		if ok {
			return rt, vals
		}
	}
	return nil, nil
}

// noSamples reports a validated sample filter (sampleIds XOR sampleListId)
// that selects no sample: the sample list "" (or a sampleListId Jackson
// coerced from a number), which Java looks up and finds empty.
func noSamples(sampleIDs []string, sampleListID string) bool {
	return len(sampleIDs) == 0 && sampleListID == ""
}

// blankSampleList reports a sampleListId request parameter given blank: a
// String parameter, so present, naming no list.
func blankSampleList(q url.Values) bool {
	return q.Has("sampleListId") && q.Get("sampleListId") == ""
}

// ---------- existence ----------

// missing returns Java's 404 message when the path entity of kind does not
// exist, else "". Lookup failures count as existing.
func (s *State) missing(kind string, v map[string]string) string {
	if s.CH == nil {
		return ""
	}
	study := v["studyId"]
	switch kind {
	case "study":
		if !s.studyKnown(study) {
			return msgStudyNotFound(study)
		}
	case "patient":
		if !s.rowExists("SELECT 1 FROM patient WHERE study_id = {study:String} AND patient_id = {patient:String} LIMIT 1",
			ch.Params{"study": study, "patient": v["patientId"]}) {
			return msgPatientNotFound(study, v["patientId"])
		}
	case "sample":
		if !s.rowExists("SELECT 1 FROM sample WHERE study_id = {study:String} AND sample_id = {sample:String} LIMIT 1",
			ch.Params{"study": study, "sample": v["sampleId"]}) {
			return msgSampleNotFound(study, v["sampleId"])
		}
	case "sampleList":
		if !s.rowExists("SELECT 1 FROM sample_list WHERE list_id = {list:String} LIMIT 1", ch.Params{"list": v["sampleListId"]}) {
			return msgSampleListNotFound(v["sampleListId"])
		}
	case "profile":
		if !s.rowExists("SELECT 1 FROM molecular_profile WHERE molecular_profile_id = {profile:String} LIMIT 1",
			ch.Params{"profile": v["molecularProfileId"]}) {
			return msgMolecularProfileNotFound(v["molecularProfileId"])
		}
	}
	return ""
}

func (s *State) studyKnown(id string) bool {
	type row struct {
		StudyID string `json:"studyId"`
	}
	rows, err := cachedQuery[row](s, "SELECT study_id AS studyId FROM cancer_study")
	if err != nil {
		return true
	}
	for _, r := range rows {
		if r.StudyID == id {
			return true
		}
	}
	return false
}

// rowExists reports whether sql (a "SELECT 1 ... LIMIT 1") returns a row.
func (s *State) rowExists(sql string, p ch.Params) bool {
	type row struct {
		One int `json:"one"`
	}
	rows, err := ch.QueryP[row](s.CH, strings.Replace(sql, "SELECT 1 ", "SELECT 1 AS one ", 1), p)
	return err != nil || len(rows) > 0
}

// ---------- response shaping ----------

// legacyStudy is the legacy CancerStudy nested in DETAILED patients
// (cancerStudy) and molecular profiles (study): the summary columns of
// PatientMapper / MolecularProfileMapper.
type legacyStudy struct {
	StudyID         string `json:"studyId"`
	CancerTypeID    string `json:"cancerTypeId"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	PublicStudy     bool   `json:"publicStudy"`
	Pmid            string `json:"pmid,omitempty"`
	Citation        string `json:"citation,omitempty"`
	Groups          string `json:"groups"`
	Status          int    `json:"status"`
	ImportDate      string `json:"importDate,omitempty"`
	ReferenceGenome string `json:"referenceGenome"`
	ReadPermission  bool   `json:"readPermission"`
}

// legacyStudies returns the encoded legacyStudy of each study.
func (s *State) legacyStudies() map[string]json.RawMessage {
	type row struct {
		StudyID         string `json:"studyId"`
		CancerTypeID    string `json:"cancerTypeId"`
		Name            string `json:"name"`
		Description     string `json:"description"`
		Pmid            string `json:"pmid"`
		Citation        string `json:"citation"`
		Groups          string `json:"groups"`
		ReferenceGenome string `json:"referenceGenome"`
		Public          int    `json:"public"`
	}
	out := map[string]json.RawMessage{}
	if s.CH == nil {
		return out
	}
	rows, err := cachedQuery[row](s, "SELECT study_id AS studyId, cancer_type_id AS cancerTypeId, name, description, "+
		"pmid, citation, groups, reference_genome AS referenceGenome, "+s.studyPublicSQL("")+" AS public FROM cancer_study")
	if err != nil {
		return out
	}
	dates := s.studyImportDates()
	for _, r := range rows {
		b, _ := json.Marshal(legacyStudy{
			StudyID: r.StudyID, CancerTypeID: r.CancerTypeID, Name: r.Name, Description: r.Description,
			PublicStudy: r.Public != 0, Pmid: r.Pmid, Citation: r.Citation, Groups: r.Groups,
			ImportDate: dates[r.StudyID], ReferenceGenome: referenceGenome(r.ReferenceGenome), ReadPermission: true,
		})
		out[r.StudyID] = b
	}
	return out
}

// reshaper rewrites JSON objects: when shaped, objects keep only the kept
// fields (in their order of appearance); when nest is set, each object gains
// the legacy study of its studyId under that name.
type reshaper struct {
	kept    map[string]bool
	shaped  bool
	nest    string
	studies map[string]json.RawMessage
	fields  []objectField
}

// byteWriter is a bytes.Buffer or a bufio.Writer.
type byteWriter interface {
	io.Writer
	io.ByteWriter
}

func (s *State) newReshaper(keep []string, shaped bool, nest string) *reshaper {
	rs := &reshaper{kept: make(map[string]bool, len(keep)), shaped: shaped, nest: nest}
	for _, k := range keep {
		rs.kept[k] = true
	}
	if nest != "" {
		rs.studies = s.legacyStudies()
	}
	return rs
}

// object writes obj reshaped to out. It writes nothing and returns false
// when obj is not a JSON object.
func (rs *reshaper) object(out byteWriter, obj []byte) bool {
	var ok bool
	rs.fields, ok = objectFields(obj, rs.fields[:0])
	if !ok {
		return false
	}
	out.WriteByte('{')
	n := 0
	studyID := ""
	for _, f := range rs.fields {
		if rs.nest != "" && string(f.key) == "studyId" {
			studyID = jsonStringValue(f.val)
		}
		if rs.shaped && !rs.kept[string(f.key)] {
			continue
		}
		if n > 0 {
			out.WriteByte(',')
		}
		out.Write(f.raw)
		out.WriteByte(':')
		out.Write(f.val)
		n++
	}
	if st, ok := rs.studies[studyID]; ok && rs.nest != "" {
		if n > 0 {
			out.WriteByte(',')
		}
		fmt.Fprintf(out, "%q:", rs.nest)
		out.Write(st)
	}
	out.WriteByte('}')
	return true
}

// reshape rewrites a whole JSON object or array of objects as reshaper
// does. ok is false when the body is not an object or array of objects.
func (s *State) reshape(body []byte, keep []string, shaped bool, nest string) ([]byte, bool) {
	rs := s.newReshaper(keep, shaped, nest)
	var out bytes.Buffer
	out.Grow(len(body))
	writeObject := func(obj []byte) bool { return rs.object(&out, obj) }
	b := bytes.TrimSpace(body)
	switch {
	case len(b) > 0 && b[0] == '{':
		if !writeObject(b) {
			return nil, false
		}
	case len(b) > 0 && b[0] == '[':
		out.WriteByte('[')
		i := skipSpace(b, 1)
		first := true
		for i < len(b) && b[i] != ']' {
			end := skipValue(b, i)
			if end <= i || b[i] != '{' {
				return nil, false
			}
			if !first {
				out.WriteByte(',')
			}
			first = false
			if !writeObject(b[i:end]) {
				return nil, false
			}
			i = skipSpace(b, end)
			if i < len(b) && b[i] == ',' {
				i = skipSpace(b, i+1)
			}
		}
		out.WriteByte(']')
	default:
		return nil, false
	}
	out.WriteByte('\n')
	return out.Bytes(), true
}

// objectField is one member of a JSON object: its decoded key, the raw
// (quoted) key and the raw value.
type objectField struct {
	key, raw, val []byte
}

// objectFields splits a JSON object into its members, appending them to
// fields (which callers may reuse between objects).
func objectFields(b []byte, fields []objectField) ([]objectField, bool) {
	if len(b) < 2 || b[0] != '{' {
		return nil, false
	}
	i := skipSpace(b, 1)
	for i < len(b) && b[i] != '}' {
		if b[i] != '"' {
			return nil, false
		}
		kend := skipValue(b, i)
		raw := b[i:kend]
		if len(raw) < 2 || raw[len(raw)-1] != '"' {
			return nil, false
		}
		key := raw[1 : len(raw)-1]
		if bytes.IndexByte(key, '\\') >= 0 {
			var k string
			if json.Unmarshal(raw, &k) != nil {
				return nil, false
			}
			key = []byte(k)
		}
		i = skipSpace(b, kend)
		if i >= len(b) || b[i] != ':' {
			return nil, false
		}
		i = skipSpace(b, i+1)
		vend := skipValue(b, i)
		if vend <= i {
			return nil, false
		}
		fields = append(fields, objectField{key: key, raw: raw, val: b[i:vend]})
		i = skipSpace(b, vend)
		if i < len(b) && b[i] == ',' {
			i = skipSpace(b, i+1)
		}
	}
	return fields, true
}

// jsonStringValue returns the string a raw JSON value holds, or "" when it is
// not a string.
func jsonStringValue(v []byte) string {
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' && bytes.IndexByte(v, '\\') < 0 {
		return string(v[1 : len(v)-1])
	}
	var s string
	json.Unmarshal(v, &s)
	return s
}

func skipSpace(b []byte, i int) int {
	for i < len(b) && (b[i] == ' ' || b[i] == '\n' || b[i] == '\t' || b[i] == '\r') {
		i++
	}
	return i
}

// skipValue returns the index just past the JSON value starting at b[i]
// (which must be valid JSON, as produced by encoding/json).
func skipValue(b []byte, i int) int {
	if i >= len(b) {
		return i
	}
	switch b[i] {
	case '"':
		for j := i + 1; j < len(b); j++ {
			switch b[j] {
			case '\\':
				j++
			case '"':
				return j + 1
			}
		}
		return len(b)
	case '{', '[':
		depth := 0
		for j := i; j < len(b); j++ {
			switch b[j] {
			case '"':
				j = skipValue(b, j) - 1
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return j + 1
				}
			}
		}
		return len(b)
	default:
		j := i
		for ; j < len(b); j++ {
			switch b[j] {
			case ',', '}', ']', ' ', '\n', '\t', '\r':
				return j
			}
		}
		return j
	}
}
