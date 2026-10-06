package contract

import "testing"

// The field order of responses without a projection, as Jackson writes the
// Java models (see projection_order_test.go). These hold for the Java
// backend too.

func TestOracleFieldOrder(t *testing.T) {
	requireAPI(t)
	studyView := `{"studyIds":["study_es_0"]}`
	lgg := `{"studyIds":["lgg_ucsf_2014_test_generic_assay"]}`
	cases := []struct {
		method, url, body string
		path              []string
		want              string
	}{
		{"POST", "/api/studies/tags/fetch", `["study_es_0"]`, nil, "cancerStudyId,studyId,tags"},
		{"POST", "/api/sample-lists-counts/fetch", studyView, nil, "label,value,count"},
		{"POST", "/api/molecular-profile-sample-counts/fetch", studyView, nil, "label,value,count"},
		{"POST", "/api/mutated-genes/fetch", studyView, nil,
			"numberOfAlteredCases,numberOfAlteredCasesOnPanel,totalCount,numberOfProfiledCases,matchingGenePanelIds,entrezGeneId," +
				"hugoGeneSymbol,entrezGeneIds,hugoGeneSymbols,uniqueEventKey"},
		{"POST", "/api/structuralvariant-counts/fetch", studyView, nil,
			"numberOfAlteredCases,totalCount,numberOfProfiledCases,matchingGenePanelIds,gene1EntrezGeneId,gene1HugoGeneSymbol," +
				"gene2EntrezGeneId,gene2HugoGeneSymbol,hugoGeneSymbols,entrezGeneIds,uniqueEventKey"},
		{"POST", "/api/clinical-data-bin-counts/fetch?dataBinMethod=STATIC",
			`{"attributes":[{"attributeId":"MUTATION_COUNT"}],"studyViewFilter":{"studyIds":["study_es_0"]}}`, nil, "start,end,count,attributeId"},
		{"POST", "/api/clinical-data-violin-plots/fetch?categoricalAttributeId=CANCER_TYPE&numericalAttributeId=MUTATION_COUNT",
			lgg, nil, "rows,axisStart,axisEnd"},
		{"POST", "/api/clinical-data-violin-plots/fetch?categoricalAttributeId=CANCER_TYPE&numericalAttributeId=MUTATION_COUNT",
			lgg, []string{"rows"}, "category,numSamples,curveData,boxData,individualPoints"},
		{"POST", "/api/clinical-data-violin-plots/fetch?categoricalAttributeId=CANCER_TYPE&numericalAttributeId=MUTATION_COUNT",
			lgg, []string{"rows", "boxData"}, "whiskerLower,whiskerUpper,median,q1,q3"},
		{"POST", "/api/molecular-profiles/study_es_0_mutations/variant-counts/fetch", `[{"entrezGeneId":7157,"keyword":"TP53 truncating"},{"entrezGeneId":672,"keyword":"BRCA1 truncating"}]`, nil,
			"molecularProfileId,entrezGeneId,keyword,numberOfSamples,numberOfSamplesWithMutationInGene,numberOfSamplesWithKeyword"},
		{"POST", "/api/mutation-counts-by-position/fetch", `[{"entrezGeneId":7157,"proteinPosEnd":273,"proteinPosStart":273}]`, nil,
			"entrezGeneId,proteinPosStart,proteinPosEnd,count"},
		{"POST", "/api/gene-panel-data/fetch", `{"molecularProfileIds":["ascn_test_study_mutations"]}`, nil,
			"uniqueSampleKey,uniquePatientKey,molecularProfileId,sampleId,patientId,studyId,profiled"},
		{"GET", "/api/reference-genome-genes/hg19/7157", "", nil, "referenceGenomeId,entrezGeneId,hugoGeneSymbol,chromosome,cytoband,start,end"},
	}
	for _, c := range cases {
		got := fieldOrder(t, c.method, c.url, c.body, c.path...)
		wantEq(t, got, split(c.want))
	}
}
