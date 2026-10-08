package api

import (
	"strings"
)

// addedPaging is an endpoint go pages and sorts although its Java
// controller declares no paging parameters. The parameters are optional:
// without them the response is the whole list in the endpoint's own order.
// They are validated as the Java list endpoints validate theirs (pageSize
// 1..10,000,000, pageNumber from 0, sortBy and direction enums) and
// documented in the served API descriptions (apiDocsOverlay).
type addedPaging struct {
	method string
	// path is the endpoint's path as the API descriptions name it.
	path string
	// sort is the sortBy parameter: an enum of the response model's fields.
	// Without values the endpoint pages only (no sortBy or direction).
	sort param
	// defaultSort is the field a page without sortBy is sorted by, for an
	// in-memory list whose own order is not stable. Empty keeps the
	// endpoint's order.
	defaultSort string
	// numeric lists fields that hold numbers as JSON strings: an in-memory
	// sort compares them as numbers, and values that are not numbers come
	// last, as a SQL sort over toFloat64OrNull orders them.
	numeric []string
}

// addedSortBy is a go sortBy enum, named after the response model.
func addedSortBy(model string, fields ...string) param {
	return sortBy(model+"SortBy", fields...)
}

var (
	molecularDataSortParam     = addedSortBy("MolecularData", "entrezGeneId", "sampleId", "patientId", "studyId", "molecularProfileId", "value")
	discreteCopyNumberSort     = addedSortBy("DiscreteCopyNumberData", "entrezGeneId", "sampleId", "patientId", "alteration")
	genericAssayDataSort       = addedSortBy("GenericAssayData", "stableId", "genericAssayStableId", "sampleId", "patientId", "studyId", "molecularProfileId", "value")
	genePanelDataSort          = addedSortBy("GenePanelData", "molecularProfileId", "sampleId", "patientId", "studyId", "genePanelId", "profiled")
	filteredSampleSort         = addedSortBy("FilteredSample", "sampleId", "patientId", "studyId")
	structuralVariantSortParam = addedSortBy("StructuralVariant", "sampleId", "patientId", "studyId", "molecularProfileId",
		"site1EntrezGeneId", "site1HugoSymbol", "site1Chromosome", "site1Position",
		"site2EntrezGeneId", "site2HugoSymbol", "site2Chromosome", "site2Position",
		"variantClass", "eventInfo", "svStatus", "tumorVariantCount", "tumorReadCount")
	genesetSort              = addedSortBy("Geneset", "genesetId", "name", "description")
	genericAssayMetaSort     = addedSortBy("GenericAssayMeta", "stableId", "entityType")
	referenceGenomeGeneSort  = addedSortBy("ReferenceGenomeGene", "entrezGeneId", "hugoGeneSymbol", "chromosome", "cytoband", "start", "end")
	sampleTreatmentSort      = addedSortBy("SampleTreatment", "treatment", "time", "count")
	patientTreatmentSort     = addedSortBy("PatientTreatment", "treatment", "count")
	namespaceDataSort        = addedSortBy("NamespaceData", "outerKey", "innerKey", "sampleId", "patientId", "studyId", "attrValue")
	genesetMolecularDataSort = addedSortBy("GenesetMolecularData", "genesetId", "sampleId", "patientId", "value")
	coExpressionSort         = addedSortBy("CoExpression", "geneticEntityId", "spearmansCorrelation", "pValue")
	genomicEnrichmentSort    = addedSortBy("GenomicEnrichment", "entrezGeneId", "hugoGeneSymbol", "pValue")
	alterationEnrichmentSort = addedSortBy("AlterationEnrichment", "entrezGeneId", "hugoGeneSymbol", "pValue")
	gaEnrichmentSort         = addedSortBy("GenericAssayEnrichment", "stableId", "pValue")
	noSort                   = param{}
)

// addedPagedEndpoints are the endpoints with added paging. Lists that
// stream from ClickHouse read the page in SQL (claimListPage); the others
// are built in memory and paged by pageList.
var addedPagedEndpoints = []addedPaging{
	// Clinical data, samples, patients.
	{method: "POST", path: "/api/clinical-data/fetch", sort: clinicalDataSort},
	{method: "POST", path: "/api/studies/{studyId}/clinical-data/fetch", sort: clinicalDataSort},
	{method: "POST", path: "/api/samples/fetch", sort: sampleSort},
	{method: "POST", path: "/api/patients/fetch", sort: patientSort},
	{method: "POST", path: "/api/filtered-samples/fetch", sort: filteredSampleSort},
	{method: "GET", path: "/api/sample-lists/{sampleListId}/sample-ids", sort: noSort},
	{method: "POST", path: "/api/clinical-attributes/fetch", sort: clinicalAttrSort},
	{method: "POST", path: "/api/survival-data/fetch", sort: clinicalDataSort},
}

// sorted reports whether the endpoint takes sortBy and direction.
func (a addedPaging) sorted() bool { return len(a.sort.values) > 0 }

// endpoint is the endpoint as pagedEndpoints names it.
func (a addedPaging) endpoint() string { return a.method + " " + templatePath(a.path) }

// params are the endpoint's paging parameters, as the contract checks
// them.
func (a addedPaging) params() []param {
	ps := []param{pageSizeParam, pageNumberParam}
	if a.sorted() {
		ps = append(ps, a.sort, directionParam)
	}
	return ps
}

// templatePath replaces each {name} segment of path with {}.
func templatePath(path string) string {
	segs := strings.Split(path, "/")
	for i, s := range segs {
		if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
			segs[i] = "{}"
		}
	}
	return strings.Join(segs, "/")
}

// addedPagingOf returns the added paging of a pagedEndpoints entry, or nil.
func addedPagingOf(endpoint string) *addedPaging {
	return addedByEndpoint[endpoint]
}

var addedByEndpoint = map[string]*addedPaging{}

func init() {
	for i := range addedPagedEndpoints {
		a := &addedPagedEndpoints[i]
		addedByEndpoint[a.endpoint()] = a
		pagedEndpoints = append(pagedEndpoints, a.endpoint())
		if !a.sorted() {
			unsortedEndpoints[a.endpoint()] = true
		}
		found := false
		for j := range contractRoutes {
			rt := &contractRoutes[j]
			if rt.method == a.method && templatePath(rt.pattern) == templatePath(a.path) {
				rt.params = append(rt.params, a.params()...)
				found = true
				break
			}
		}
		if !found {
			contractRoutes = append(contractRoutes, contractRoute{method: a.method, pattern: a.path, params: a.params()})
		}
	}
}
