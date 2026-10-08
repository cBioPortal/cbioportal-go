package core

import (
	"strings"
)

// AddedPaging is an endpoint go pages and sorts although its Java
// controller declares no paging parameters. The parameters are optional:
// without them the response is the whole list in the endpoint's own order.
// They are validated as the Java list endpoints validate theirs (pageSize
// 1..10,000,000, pageNumber from 0, sortBy and direction enums) and
// documented in the served API descriptions (apiDocsOverlay).
type AddedPaging struct {
	Method string
	// path is the endpoint's path as the API descriptions name it.
	Path string
	// sort is the sortBy parameter: an enum of the response model's fields.
	// Without values the endpoint pages only (no sortBy or direction).
	Sort Param
	// defaultSort is the field a page without sortBy is sorted by, for an
	// in-memory list whose own order is not stable. Empty keeps the
	// endpoint's order.
	DefaultSort string
	// numeric lists fields that hold numbers as JSON strings: an in-memory
	// sort compares them as numbers, and values that are not numbers come
	// last, as a SQL sort over toFloat64OrNull orders them.
	numeric []string
}

// addedSortBy is a go sortBy enum, named after the response model.
func addedSortBy(model string, fields ...string) Param {
	return sortBy(model+"SortBy", fields...)
}

// The sortBy enums go adds to the endpoints it pages (addedPagedEndpoints).
var (
	MolecularDataSortParam     = addedSortBy("MolecularData", "entrezGeneId", "sampleId", "patientId", "studyId", "molecularProfileId", "value")
	DiscreteCopyNumberSort     = addedSortBy("DiscreteCopyNumberData", "entrezGeneId", "sampleId", "patientId", "alteration")
	GenericAssayDataSort       = addedSortBy("GenericAssayData", "stableId", "genericAssayStableId", "sampleId", "patientId", "studyId", "molecularProfileId", "value")
	genePanelDataSort          = addedSortBy("GenePanelData", "molecularProfileId", "sampleId", "patientId", "studyId", "genePanelId", "profiled")
	FilteredSampleSort         = addedSortBy("FilteredSample", "sampleId", "patientId", "studyId")
	StructuralVariantSortParam = addedSortBy("StructuralVariant", "sampleId", "patientId", "studyId", "molecularProfileId",
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
	noSort                   = Param{}
)

// AddedPagedEndpoints are the endpoints with added paging. Lists that
// stream from ClickHouse read the page in SQL (claimListPage); the others
// are built in memory and paged by pageList.
var AddedPagedEndpoints = []AddedPaging{
	// Clinical data, samples, patients.
	{Method: "POST", Path: "/api/clinical-data/fetch", Sort: ClinicalDataSort},
	{Method: "POST", Path: "/api/studies/{studyId}/clinical-data/fetch", Sort: ClinicalDataSort},
	{Method: "POST", Path: "/api/samples/fetch", Sort: SampleSort},
	{Method: "POST", Path: "/api/patients/fetch", Sort: PatientSort},
	{Method: "POST", Path: "/api/filtered-samples/fetch", Sort: FilteredSampleSort},
	{Method: "GET", Path: "/api/sample-lists/{sampleListId}/sample-ids", Sort: noSort},
	{Method: "POST", Path: "/api/clinical-attributes/fetch", Sort: clinicalAttrSort},
	{Method: "POST", Path: "/api/survival-data/fetch", Sort: ClinicalDataSort},

	// Molecular data.
	{Method: "POST", Path: "/api/molecular-profiles/{molecularProfileId}/molecular-data/fetch", Sort: MolecularDataSortParam},
	{Method: "GET", Path: "/api/molecular-profiles/{molecularProfileId}/molecular-data", Sort: MolecularDataSortParam},
	{Method: "POST", Path: "/api/molecular-data/fetch", Sort: MolecularDataSortParam},
	{Method: "POST", Path: "/api/molecular-profiles/{molecularProfileId}/discrete-copy-number/fetch", Sort: DiscreteCopyNumberSort},
	{Method: "GET", Path: "/api/molecular-profiles/{molecularProfileId}/discrete-copy-number", Sort: DiscreteCopyNumberSort},
	{Method: "POST", Path: "/api/generic_assay_data/fetch", Sort: GenericAssayDataSort},
	{Method: "POST", Path: "/api/generic_assay_data/{molecularProfileId}/fetch", Sort: GenericAssayDataSort},
	{Method: "GET", Path: "/api/generic-assay-data/{molecularProfileId}/generic-assay/{genericAssayStableId}", Sort: GenericAssayDataSort},
	{Method: "POST", Path: "/api/gene-panel-data/fetch", Sort: genePanelDataSort},
	{Method: "POST", Path: "/api/molecular-profiles/{molecularProfileId}/gene-panel-data/fetch", Sort: genePanelDataSort},
	{Method: "POST", Path: "/api/copy-number-segments/fetch", Sort: CopyNumberSegmentSort},
	{Method: "POST", Path: "/api/structural-variant/fetch", Sort: StructuralVariantSortParam},
	{Method: "POST", Path: "/api/genetic-profiles/{geneticProfileId}/geneset-genetic-data/fetch", Sort: genesetMolecularDataSort,
		numeric: []string{"value"}},
	{Method: "POST", Path: "/api/namespace-data/fetch", Sort: namespaceDataSort},

	// Genes, gene sets, generic assay entities.
	{Method: "POST", Path: "/api/genes/fetch", Sort: geneSort, DefaultSort: "entrezGeneId"},
	{Method: "POST", Path: "/api/genesets/fetch", Sort: genesetSort},
	{Method: "GET", Path: "/api/generic-assay-meta/{molecularProfileId}", Sort: genericAssayMetaSort},
	{Method: "GET", Path: "/api/reference-genome-genes/{genomeName}", Sort: referenceGenomeGeneSort},
	{Method: "POST", Path: "/api/reference-genome-genes/{genomeName}/fetch", Sort: referenceGenomeGeneSort},

	// Study view and group comparison lists.
	{Method: "POST", Path: "/api/treatments/sample", Sort: sampleTreatmentSort},
	{Method: "POST", Path: "/api/treatments/patient", Sort: patientTreatmentSort},
	{Method: "POST", Path: "/api/molecular-profiles/co-expressions/fetch", Sort: coExpressionSort,
		numeric: []string{"geneticEntityId"}},
	{Method: "POST", Path: "/api/expression-enrichments/fetch", Sort: genomicEnrichmentSort},
	{Method: "POST", Path: "/api/alteration-enrichments/fetch", Sort: alterationEnrichmentSort},
	{Method: "POST", Path: "/api/generic-assay-enrichments/fetch", Sort: gaEnrichmentSort},
}

// Sorted reports whether the endpoint takes sortBy and direction.
func (a AddedPaging) Sorted() bool { return len(a.Sort.Values) > 0 }

// Endpoint is the endpoint as pagedEndpoints names it.
func (a AddedPaging) Endpoint() string { return a.Method + " " + TemplatePath(a.Path) }

// params are the endpoint's paging parameters, as the contract checks
// them.
func (a AddedPaging) params() []Param {
	ps := []Param{pageSizeParam, pageNumberParam}
	if a.Sorted() {
		ps = append(ps, a.Sort, directionParam)
	}
	return ps
}

// TemplatePath replaces each {name} segment of path with {}.
func TemplatePath(path string) string {
	segs := strings.Split(path, "/")
	for i, s := range segs {
		if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
			segs[i] = "{}"
		}
	}
	return strings.Join(segs, "/")
}

// addedPagingOf returns the added paging of a pagedEndpoints entry, or nil.
func addedPagingOf(endpoint string) *AddedPaging {
	return addedByEndpoint[endpoint]
}

var addedByEndpoint = map[string]*AddedPaging{}

func init() {
	for i := range AddedPagedEndpoints {
		a := &AddedPagedEndpoints[i]
		addedByEndpoint[a.Endpoint()] = a
		PagedEndpoints = append(PagedEndpoints, a.Endpoint())
		if !a.Sorted() {
			unsortedEndpoints[a.Endpoint()] = true
		}
		found := false
		for j := range contractRoutes {
			rt := &contractRoutes[j]
			if rt.method == a.Method && TemplatePath(rt.pattern) == TemplatePath(a.Path) {
				rt.Params = append(rt.Params, a.params()...)
				found = true
				break
			}
		}
		if !found {
			contractRoutes = append(contractRoutes, contractRoute{method: a.Method, pattern: a.Path, Params: a.params()})
		}
	}
}
