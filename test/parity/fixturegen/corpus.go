package main

import (
	"maps"
	"net/url"
	"slices"
	"strings"
)

var fixtureStudies = []string{"ascn_test_study", "study_hg38", "teststudy_genepanels", "study_es_0", "lgg_ucsf_2014_test_generic_assay"}

type corpus struct{ reqs []request }

func (c *corpus) get(name, path string) {
	c.reqs = append(c.reqs, request{Name: name, Method: "GET", Path: path})
}

func (c *corpus) post(name, path string, body any) {
	c.reqs = append(c.reqs, request{Name: name, Method: "POST", Path: path, Body: body})
}

func q(s string) string { return url.QueryEscape(s) }

// allAlterations is the study view's default AlterationFilter.
func allAlterations() map[string]any {
	return map[string]any{
		"includeDriver": true, "includeVUS": true, "includeUnknownOncogenicity": true,
		"includeGermline": true, "includeSomatic": true, "includeUnknownStatus": true,
		"includeUnknownTier": true, "tiersBooleanMap": map[string]bool{},
		"copyNumberAlterationEventTypes": map[string]bool{"AMP": true, "HOMDEL": true},
		"mutationEventTypes":             map[string]bool{"any": true},
		"structuralVariants":             nil,
	}
}

func buildCorpus() []request {
	c := &corpus{}
	c.global()
	for _, s := range fixtureStudies {
		c.study(s)
	}
	c.multiStudy()
	return c.reqs
}

func (c *corpus) global() {
	c.get("studies", "/api/studies")
	c.get("studies-summary", "/api/studies?projection=SUMMARY")
	c.get("studies-detailed", "/api/studies?projection=DETAILED")
	c.get("studies-keyword", "/api/studies?keyword=study")
	c.get("studies-sorted", "/api/studies?sortBy=name&direction=DESC")
	c.post("studies-fetch", "/api/studies/fetch?projection=DETAILED", fixtureStudies)
	c.post("studies-tags-fetch", "/api/studies/tags/fetch", fixtureStudies)
	c.get("cancer-types", "/api/cancer-types")
	c.get("cancer-type-brca", "/api/cancer-types/brca")
	c.get("gene-panels", "/api/gene-panels")
	c.get("gene-panels-detailed", "/api/gene-panels?projection=DETAILED")
	c.post("gene-panels-fetch", "/api/gene-panels/fetch?projection=DETAILED", []string{"TESTPANEL1", "TESTPANEL2"})
	c.get("gene-panel-1", "/api/gene-panels/TESTPANEL1")
	c.get("gene-tp53", "/api/genes/TP53")
	c.get("gene-7157", "/api/genes/7157")
	c.get("gene-aliases", "/api/genes/7157/aliases")
	c.post("genes-fetch-symbol", "/api/genes/fetch?geneIdType=HUGO_GENE_SYMBOL", []string{"TP53", "BRCA1", "ERCC5", "NOT_A_GENE"})
	c.post("genes-fetch-entrez", "/api/genes/fetch?geneIdType=ENTREZ_GENE_ID&projection=DETAILED", []string{"7157", "672"})
	c.get("genes-keyword", "/api/genes?keyword=BRC&pageSize=10")
	c.get("genesets", "/api/genesets")
	c.get("genesets-version", "/api/genesets/version")
	c.post("genesets-fetch", "/api/genesets/fetch", []string{"GO_ATP_DEPENDENT_CHROMATIN_REMODELING", "HINATA_NFKB_MATRIX"})
	c.post("geneset-hierarchy", "/api/geneset-hierarchy/fetch?geneticProfileId=study_es_0_gsva_scores&sampleListId=study_es_0_all&percentile=75&scoreThreshold=0.4&pvalueThreshold=0.05", nil)
	c.get("refgenome-gene", "/api/reference-genome-genes/hg19/7157")
	c.get("refgenome-gene-hg38", "/api/reference-genome-genes/hg38/7157")
	c.post("refgenome-genes-fetch", "/api/reference-genome-genes/hg19/fetch", []string{"7157", "672"})
	c.get("clinical-attributes", "/api/clinical-attributes")
	c.get("molecular-profiles", "/api/molecular-profiles")
	c.get("sample-lists", "/api/sample-lists")
	c.get("samples-keyword", "/api/samples?keyword=TCGA-A1&projection=SUMMARY")
	c.get("patients-keyword", "/api/patients?keyword=P0")
	c.post("custom-driver-annotation-report", "/api/custom-driver-annotation-report/fetch", []string{"study_es_0_mutations", "study_es_0_gistic", "study_es_0_structural_variants"})
	c.post("mutation-counts-by-position", "/api/mutation-counts-by-position/fetch", []map[string]int{
		{"entrezGeneId": 7157, "proteinPosStart": 273, "proteinPosEnd": 273},
		{"entrezGeneId": 672, "proteinPosStart": 1, "proteinPosEnd": 2000},
	})
}

func (c *corpus) study(s string) {
	p := "/api/studies/" + s
	c.get(s+"/study", p)
	c.get(s+"/tags", p+"/tags")
	c.get(s+"/molecular-profiles", p+"/molecular-profiles")
	c.get(s+"/sample-lists", p+"/sample-lists?projection=DETAILED")
	c.get(s+"/samples", p+"/samples?projection=DETAILED")
	c.get(s+"/samples-summary", p+"/samples")
	c.get(s+"/patients", p+"/patients")
	c.get(s+"/clinical-attributes", p+"/clinical-attributes")
	c.get(s+"/clinical-data-sample", p+"/clinical-data?clinicalDataType=SAMPLE&projection=DETAILED")
	c.get(s+"/clinical-data-patient", p+"/clinical-data?clinicalDataType=PATIENT")
	c.get(s+"/clinical-events", p+"/clinical-events?projection=DETAILED")
	c.get(s+"/resource-definitions", p+"/resource-definitions")
	c.get(s+"/resource-data-all", p+"/resource-data-all")
	c.get(s+"/significantly-mutated-genes", p+"/significantly-mutated-genes")
	c.get(s+"/significant-copy-number-regions", p+"/significant-copy-number-regions")
	c.post(s+"/clinical-attributes-fetch", "/api/clinical-attributes/fetch?projection=DETAILED", []string{s})
	c.post(s+"/clinical-attributes-counts", "/api/clinical-attributes/counts/fetch", map[string]any{"sampleListId": s + "_all"})
	c.post(s+"/clinical-data-fetch-attr", p+"/clinical-data/fetch?clinicalDataType=SAMPLE", map[string]any{"attributeIds": []string{"CANCER_TYPE", "MUTATION_COUNT"}})
	c.post(s+"/sample-lists-fetch", "/api/sample-lists/fetch?projection=DETAILED", []string{s + "_all", s + "_sequenced"})
	c.get(s+"/sample-list-ids", "/api/sample-lists/"+s+"_all/sample-ids")
	c.post(s+"/samples-fetch-list", "/api/samples/fetch?projection=DETAILED", map[string]any{"sampleListIds": []string{s + "_all"}})

	samples := list(javaJSON("GET", p+"/samples", nil))
	patients := list(javaJSON("GET", p+"/patients", nil))
	profiles := list(javaJSON("GET", p+"/molecular-profiles", nil))
	attrs := list(javaJSON("GET", p+"/clinical-attributes", nil))
	if len(samples) == 0 {
		return
	}
	sampleIDs := []string{}
	sampleIdents := []map[string]string{}
	for _, x := range samples {
		sampleIDs = append(sampleIDs, str(x["sampleId"]))
		sampleIdents = append(sampleIdents, map[string]string{"studyId": s, "sampleId": str(x["sampleId"])})
	}
	patientIdents := []map[string]string{}
	for _, x := range patients {
		patientIdents = append(patientIdents, map[string]string{"studyId": s, "patientId": str(x["patientId"])})
	}

	// Patient view on the first patients.
	for i, pt := range patients {
		if i >= 3 {
			break
		}
		pid := str(pt["patientId"])
		pp := p + "/patients/" + q(pid)
		c.get(s+"/patient/"+pid, pp)
		c.get(s+"/patient-samples/"+pid, pp+"/samples?projection=DETAILED")
		c.get(s+"/patient-clinical/"+pid, pp+"/clinical-data?projection=DETAILED")
		c.get(s+"/patient-events/"+pid, pp+"/clinical-events?projection=DETAILED")
		c.get(s+"/patient-resource/"+pid, pp+"/resource-data")
	}
	for i, x := range samples {
		if i >= 3 {
			break
		}
		sid := str(x["sampleId"])
		c.get(s+"/sample/"+sid, p+"/samples/"+q(sid))
		c.get(s+"/sample-clinical/"+sid, p+"/samples/"+q(sid)+"/clinical-data?projection=DETAILED")
		c.get(s+"/sample-segments/"+sid, p+"/samples/"+q(sid)+"/copy-number-segments")
		c.get(s+"/sample-resource/"+sid, p+"/samples/"+q(sid)+"/resource-data")
	}
	c.post(s+"/clinical-data-fetch-ids", "/api/clinical-data/fetch?clinicalDataType=SAMPLE&projection=DETAILED",
		map[string]any{"identifiers": firstN(toEntityIDs(sampleIdents, "sampleId"), 20)})
	c.post(s+"/clinical-data-fetch-patients", "/api/clinical-data/fetch?clinicalDataType=PATIENT",
		map[string]any{"identifiers": firstN(toEntityIDs(patientIdents, "patientId"), 20)})
	c.post(s+"/samples-fetch-ids", "/api/samples/fetch", map[string]any{"sampleIdentifiers": firstN(sampleIdents, 20)})
	c.post(s+"/patients-fetch", "/api/patients/fetch?projection=DETAILED", map[string]any{"patientIdentifiers": firstN(patientIdents, 20)})
	c.post(s+"/copy-number-segments-fetch", "/api/copy-number-segments/fetch", firstN(sampleIdents, 10))
	c.post(s+"/clinical-events-meta", "/api/clinical-events-meta/fetch", map[string]any{"patientIdentifiers": patientIdents})
	c.post(s+"/resource-definitions-fetch", "/api/resource-definitions/fetch", []string{s})
	event := func(t string, attrs ...map[string]string) map[string]any {
		e := map[string]any{"eventType": t}
		if len(attrs) > 0 {
			e["attributes"] = attrs
		}
		return e
	}
	for _, sv := range []struct {
		name string
		ends [2]map[string]any
	}{
		{"specimen-status", [2]map[string]any{event("SPECIMEN"), event("STATUS")}},
		{"surgery-treatment", [2]map[string]any{event("SURGERY"), event("TREATMENT", map[string]string{"key": "AGENT", "value": "TMZ"})}},
	} {
		name, ends := sv.name, sv.ends
		for _, pos := range [][2]string{{"FIRST", "FIRST"}, {"FIRST", "LAST"}} {
			c.post(s+"/survival-"+name+"-"+pos[1], "/api/survival-data/fetch", map[string]any{
				"attributeIdPrefix": "CUSTOM", "patientIdentifiers": patientIdents,
				"startEventRequestIdentifier":    map[string]any{"clinicalEventRequests": []any{ends[0]}, "position": pos[0]},
				"endEventRequestIdentifier":      map[string]any{"clinicalEventRequests": []any{ends[1]}, "position": pos[1]},
				"censoredEventRequestIdentifier": map[string]any{"clinicalEventRequests": []any{}, "position": "LAST"}})
		}
	}

	// Molecular profiles: results view and group comparison.
	profileIDs := []string{}
	var mutProfile, cnaProfile, svProfile, mrnaProfile string
	var gaProfiles []map[string]any
	for _, pr := range profiles {
		id := str(pr["molecularProfileId"])
		profileIDs = append(profileIDs, id)
		c.get(s+"/profile/"+id, "/api/molecular-profiles/"+id)
		switch str(pr["molecularAlterationType"]) {
		case "MUTATION_EXTENDED":
			mutProfile = id
		case "COPY_NUMBER_ALTERATION":
			if str(pr["datatype"]) == "DISCRETE" {
				cnaProfile = id
			}
		case "STRUCTURAL_VARIANT":
			svProfile = id
		case "MRNA_EXPRESSION":
			if mrnaProfile == "" {
				mrnaProfile = id
			}
		case "GENERIC_ASSAY":
			gaProfiles = append(gaProfiles, pr)
		}
	}
	c.post(s+"/molecular-profiles-fetch", "/api/molecular-profiles/fetch?projection=DETAILED", map[string]any{"studyIds": []string{s}})
	c.post(s+"/gene-panel-data", "/api/gene-panel-data/fetch", map[string]any{"molecularProfileIds": profileIDs})
	c.post(s+"/gene-panel-data-ids", "/api/gene-panel-data/fetch", map[string]any{"sampleMolecularIdentifiers": sampleMolIDs(firstNs(sampleIDs, 10), profileIDs)})

	genes := []int{7157, 672, 675, 2064, 3845, 4609, 238, 673, 2068, 116983, 5728, 1956}
	if mutProfile != "" {
		mp := "/api/molecular-profiles/" + mutProfile
		c.get(s+"/mutations-get", mp+"/mutations?sampleListId="+s+"_all&projection=DETAILED")
		c.post(s+"/mutations-fetch", mp+"/mutations/fetch?projection=DETAILED", map[string]any{"sampleListId": s + "_all"})
		c.post(s+"/mutations-fetch-genes", "/api/mutations/fetch?projection=DETAILED", map[string]any{"molecularProfileIds": []string{mutProfile}, "entrezGeneIds": genes})
		c.post(s+"/mutations-fetch-ids", "/api/mutations/fetch", map[string]any{"sampleMolecularIdentifiers": sampleMolIDs(firstNs(sampleIDs, 20), []string{mutProfile})})
		c.post(s+"/mutations-meta", "/api/mutations/fetch?projection=META", map[string]any{"molecularProfileIds": []string{mutProfile}})
		c.post(s+"/mutation-spectrums", mp+"/mutation-spectrums/fetch", map[string]any{"sampleListId": s + "_all"})
		c.post(s+"/variant-counts", mp+"/variant-counts/fetch", []map[string]any{{"entrezGeneId": 7157, "keyword": "TP53 truncating"}, {"entrezGeneId": 672, "keyword": "BRCA1 truncating"}})
	}
	if cnaProfile != "" {
		cp := "/api/molecular-profiles/" + cnaProfile
		c.get(s+"/dcn-get", cp+"/discrete-copy-number?sampleListId="+s+"_all&projection=DETAILED")
		for _, ev := range []string{"HOMDEL_AND_AMP", "ALL", "AMP", "DIPLOID"} {
			c.post(s+"/dcn-fetch-"+ev, cp+"/discrete-copy-number/fetch?discreteCopyNumberEventType="+ev, map[string]any{"sampleListId": s + "_all", "entrezGeneIds": genes})
		}
		c.post(s+"/dcn-counts", cp+"/discrete-copy-number-counts/fetch", []map[string]any{{"entrezGeneId": 116983, "alteration": 2}, {"entrezGeneId": 7157, "alteration": -2}})
		c.post(s+"/molecular-data-cna", cp+"/molecular-data/fetch", map[string]any{"sampleListId": s + "_all", "entrezGeneIds": genes})
	}
	if svProfile != "" {
		c.post(s+"/sv-fetch", "/api/structural-variant/fetch", map[string]any{"molecularProfileIds": []string{svProfile}})
		c.post(s+"/sv-fetch-genes", "/api/structural-variant/fetch", map[string]any{"molecularProfileIds": []string{svProfile}, "entrezGeneIds": []int{238, 673}})
		c.post(s+"/sv-fetch-query", "/api/structural-variant/fetch", map[string]any{"molecularProfileIds": []string{svProfile},
			"structuralVariantQueries": []map[string]any{{"gene1": map[string]any{"entrezId": 238, "hugoSymbol": "ALK"}, "gene2": map[string]any{"specialValue": "ANY_GENE"}}}})
	}
	for _, pr := range profiles {
		id := str(pr["molecularProfileId"])
		switch str(pr["molecularAlterationType"]) {
		case "MRNA_EXPRESSION", "METHYLATION", "PROTEIN_LEVEL":
			c.post(s+"/molecular-data/"+id, "/api/molecular-profiles/"+id+"/molecular-data/fetch?projection=DETAILED", map[string]any{"sampleListId": s + "_all", "entrezGeneIds": genes})
		case "COPY_NUMBER_ALTERATION":
			c.post(s+"/molecular-data/"+id, "/api/molecular-data/fetch", map[string]any{"molecularProfileIds": []string{id}, "entrezGeneIds": genes})
		}
	}
	if mrnaProfile != "" {
		c.post(s+"/mrna-percentile", "/api/molecular-profiles/"+mrnaProfile+"/mrna-percentile/fetch?sampleId="+q(sampleIDs[0]), genes)
		c.post(s+"/co-expressions", "/api/molecular-profiles/co-expressions/fetch?molecularProfileIdA="+mrnaProfile+"&molecularProfileIdB="+mrnaProfile+"&threshold=0",
			map[string]any{"sampleListId": s + "_all", "entrezGeneId": 7157})
	}
	for _, pr := range gaProfiles {
		id := str(pr["molecularProfileId"])
		c.get(s+"/ga-meta/"+id, "/api/generic-assay-meta/"+id)
		meta := list(javaJSON("GET", "/api/generic-assay-meta/"+id, nil))
		if len(meta) == 0 {
			continue
		}
		stable := str(meta[0]["stableId"])
		c.get(s+"/ga-meta-entity/"+stable, "/api/generic-assay-meta/generic-assay/"+q(stable))
		c.get(s+"/ga-data/"+id, "/api/generic-assay-data/"+id+"/generic-assay/"+q(stable)+"?projection=DETAILED")
		c.post(s+"/ga-data-fetch/"+id, "/api/generic_assay_data/"+id+"/fetch", map[string]any{"sampleListId": s + "_all", "genericAssayStableIds": []string{stable}})
		c.post(s+"/ga-meta-fetch/"+id, "/api/generic_assay_meta/fetch", map[string]any{"molecularProfileIds": []string{id}})
		if strings.HasSuffix(id, "_gsva_scores") {
			continue
		}
	}

	// Group comparison: split samples into halves.
	half := len(samples) / 2
	if half > 0 {
		a, b := sampleIDs[:half], sampleIDs[half:]
		for _, pid := range []string{mutProfile, cnaProfile, svProfile} {
			if pid == "" {
				continue
			}
			c.post(s+"/alteration-enrichments/"+pid, "/api/alteration-enrichments/fetch?enrichmentType=SAMPLE", map[string]any{
				"molecularProfileCasesGroupFilter": []any{molGroup("a", a, pid), molGroup("b", b, pid)},
				"alterationEventTypes": map[string]any{
					"copyNumberAlterationEventTypes": []string{"AMP", "HOMDEL"},
					"mutationEventTypes":             []string{"any"},
					"structuralVariants":             true, "includeDriver": true, "includeVUS": true,
					"includeUnknownOncogenicity": true, "includeGermline": true, "includeSomatic": true,
					"includeUnknownStatus": true, "includeUnknownTier": true, "tiersBooleanMap": map[string]bool{},
				}})
		}
		if mutProfile != "" && cnaProfile != "" {
			c.post(s+"/alteration-enrichments-combined", "/api/alteration-enrichments/fetch?enrichmentType=SAMPLE", map[string]any{
				"molecularProfileCasesGroupFilter": []any{molGroup2("a", a, mutProfile, cnaProfile), molGroup2("b", b, mutProfile, cnaProfile)},
				"alterationEventTypes": map[string]any{
					"copyNumberAlterationEventTypes": []string{"AMP", "HOMDEL"}, "mutationEventTypes": []string{"any"},
					"includeDriver": true, "includeVUS": true, "includeUnknownOncogenicity": true, "includeGermline": true,
					"includeSomatic": true, "includeUnknownStatus": true, "includeUnknownTier": true, "tiersBooleanMap": map[string]bool{},
				}})
		}
		for _, pr := range profiles {
			id := str(pr["molecularProfileId"])
			switch str(pr["molecularAlterationType"]) {
			case "MRNA_EXPRESSION", "METHYLATION", "PROTEIN_LEVEL", "COPY_NUMBER_ALTERATION":
				if str(pr["datatype"]) == "DISCRETE" {
					continue
				}
				c.post(s+"/expression-enrichments/"+id, "/api/expression-enrichments/fetch?enrichmentType=SAMPLE", []any{molGroup("a", a, id), molGroup("b", b, id)})
			case "GENERIC_ASSAY":
				ep := "/api/generic-assay-enrichments/fetch"
				switch str(pr["datatype"]) {
				case "CATEGORICAL":
					ep = "/api/generic-assay-categorical-enrichments/fetch"
				case "BINARY":
					ep = "/api/generic-assay-binary-enrichments/fetch"
				}
				c.post(s+"/ga-enrichments/"+id, ep+"?enrichmentType=SAMPLE", []any{molGroup("a", a, id), molGroup("b", b, id)})
			}
		}
		c.post(s+"/clinical-data-enrichments", "/api/clinical-data-enrichments/fetch", map[string]any{"groups": []any{
			map[string]any{"name": "a", "sampleIdentifiers": sampleIdentsOf(s, a)},
			map[string]any{"name": "b", "sampleIdentifiers": sampleIdentsOf(s, b)},
		}})
	}

	// Treatments and timelines.
	c.post(s+"/treatments-display-patient", "/api/treatments/display-patient", []string{s})
	c.post(s+"/treatments-display-sample", "/api/treatments/display-sample", []string{s})
	c.post(s+"/treatments-patient", "/api/treatments/patient?tier=Agent", map[string]any{"studyIds": []string{s}})
	c.post(s+"/treatments-sample", "/api/treatments/sample?tier=Agent", map[string]any{"studyIds": []string{s}})

	c.studyView(s, attrs, mutProfile, cnaProfile, svProfile, gaProfiles)
}

func (c *corpus) studyView(s string, attrs []map[string]any, mutProfile, cnaProfile, svProfile string, gaProfiles []map[string]any) {
	base := map[string]any{"studyIds": []string{s}, "alterationFilter": allAlterations()}
	var catAttrs, numAttrs []map[string]any
	for _, a := range attrs {
		id := str(a["clinicalAttributeId"])
		if str(a["datatype"]) == "NUMBER" {
			numAttrs = append(numAttrs, map[string]any{"attributeId": id})
		} else {
			catAttrs = append(catAttrs, map[string]any{"attributeId": id})
		}
	}
	for _, id := range []string{"SAMPLE_COUNT", "MUTATION_COUNT", "FRACTION_GENOME_ALTERED"} {
		catAttrs = append(catAttrs, map[string]any{"attributeId": id})
	}

	filters := map[string]map[string]any{"base": base}
	with := func(k string, v any) map[string]any {
		m := maps.Clone(base)
		m[k] = v
		return m
	}
	// Derive filter values from the oracle's own chart data.
	if counts := list(javaJSON("POST", "/api/clinical-data-counts/fetch", map[string]any{"attributes": catAttrs, "studyViewFilter": base})); len(counts) > 0 {
		for _, cnt := range counts {
			vals := list(cnt["counts"])
			if len(vals) >= 2 {
				filters["clinical-cat"] = with("clinicalDataFilters", []any{map[string]any{"attributeId": cnt["attributeId"], "values": []any{map[string]any{"value": vals[0]["value"]}}}})
				break
			}
		}
	}
	if len(numAttrs) > 0 {
		bins := list(javaJSON("POST", "/api/clinical-data-bin-counts/fetch?dataBinMethod=STATIC", map[string]any{"attributes": numAttrs[:1], "studyViewFilter": base}))
		for _, b := range bins {
			if b["start"] != nil && b["end"] != nil {
				filters["clinical-num"] = with("clinicalDataFilters", []any{map[string]any{"attributeId": numAttrs[0]["attributeId"], "values": []any{map[string]any{"start": b["start"], "end": b["end"]}}}})
				break
			}
		}
	}
	if mutProfile != "" {
		if g := list(javaJSON("POST", "/api/mutated-genes/fetch", base)); len(g) > 0 {
			filters["gene-mut"] = with("geneFilters", []any{map[string]any{"molecularProfileIds": []string{mutProfile},
				"geneQueries": [][]any{{map[string]any{"hugoGeneSymbol": g[0]["hugoGeneSymbol"], "entrezGeneId": g[0]["entrezGeneId"], "alterations": []string{}}}}}})
		}
	}
	if cnaProfile != "" {
		if g := list(javaJSON("POST", "/api/cna-genes/fetch", base)); len(g) > 0 {
			alt := "AMP"
			if str(g[0]["alteration"]) == "-2" {
				alt = "HOMDEL"
			}
			filters["gene-cna"] = with("geneFilters", []any{map[string]any{"molecularProfileIds": []string{cnaProfile},
				"geneQueries": [][]any{{map[string]any{"hugoGeneSymbol": g[0]["hugoGeneSymbol"], "entrezGeneId": g[0]["entrezGeneId"], "alterations": []string{alt}}}}}})
			filters["genomic-cna"] = with("genomicDataFilters", []any{map[string]any{"hugoGeneSymbol": g[0]["hugoGeneSymbol"],
				"profileType": strings.TrimPrefix(cnaProfile, s+"_"), "values": []any{map[string]any{"value": str(g[0]["alteration"])}}}})
		}
	}
	if svProfile != "" {
		if g := list(javaJSON("POST", "/api/structuralvariant-genes/fetch", base)); len(g) > 0 {
			filters["gene-sv"] = with("geneFilters", []any{map[string]any{"molecularProfileIds": []string{svProfile},
				"geneQueries": [][]any{{map[string]any{"hugoGeneSymbol": g[0]["hugoGeneSymbol"], "entrezGeneId": g[0]["entrezGeneId"], "alterations": []string{}}}}}})
		}
		if g := list(javaJSON("POST", "/api/structuralvariant-counts/fetch", base)); len(g) > 0 {
			filters["sv-pair"] = with("structuralVariantFilters", []any{map[string]any{"molecularProfileIds": []string{svProfile},
				"structVarQueries": [][]any{{map[string]any{
					"gene1Query":    map[string]any{"hugoSymbol": g[0]["gene1HugoGeneSymbol"], "specialValue": nil},
					"gene2Query":    map[string]any{"hugoSymbol": g[0]["gene2HugoGeneSymbol"], "specialValue": nil},
					"includeDriver": true, "includeVUS": true, "includeUnknownOncogenicity": true,
					"tiersBooleanMap": map[string]bool{}, "includeUnknownTier": true,
					"includeGermline": true, "includeSomatic": true, "includeUnknownStatus": true}}}}})
		}
	}
	if lists := list(javaJSON("POST", "/api/sample-lists-counts/fetch", base)); len(lists) > 1 {
		filters["case-list"] = with("caseLists", [][]string{{str(lists[len(lists)-1]["value"])}})
	}
	var gaChart []map[string]any
	for _, pr := range gaProfiles {
		id := str(pr["molecularProfileId"])
		meta := list(javaJSON("GET", "/api/generic-assay-meta/"+id+"?projection=ID", nil))
		if len(meta) == 0 {
			continue
		}
		stable := str(meta[0]["stableId"])
		pt := strings.TrimPrefix(id, s+"_")
		if str(pr["datatype"]) == "LIMIT-VALUE" {
			gaChart = append(gaChart, map[string]any{"stableId": stable, "profileType": pt, "kind": "num"})
		} else {
			gaChart = append(gaChart, map[string]any{"stableId": stable, "profileType": pt, "kind": "cat"})
		}
	}
	for _, g := range gaChart {
		if g["kind"] == "num" {
			bins := list(javaJSON("POST", "/api/generic-assay-data-bin-counts/fetch?dataBinMethod=STATIC", map[string]any{
				"genericAssayDataBinFilters": []any{map[string]any{"stableId": g["stableId"], "profileType": g["profileType"]}}, "studyViewFilter": base}))
			for _, b := range bins {
				if b["start"] != nil && b["end"] != nil {
					filters["ga-num"] = with("genericAssayDataFilters", []any{map[string]any{"stableId": g["stableId"], "profileType": g["profileType"],
						"values": []any{map[string]any{"start": b["start"], "end": b["end"]}}}})
					break
				}
			}
			break
		}
	}
	for _, g := range gaChart {
		if g["kind"] == "cat" {
			cnts := list(javaJSON("POST", "/api/generic-assay-data-counts/fetch", map[string]any{
				"genericAssayDataFilters": []any{map[string]any{"stableId": g["stableId"], "profileType": g["profileType"]}}, "studyViewFilter": base}))
			if len(cnts) > 0 {
				if vals := list(cnts[0]["counts"]); len(vals) > 0 {
					filters["ga-cat"] = with("genericAssayDataFilters", []any{map[string]any{"stableId": g["stableId"], "profileType": g["profileType"],
						"values": []any{map[string]any{"value": vals[0]["value"]}}}})
				}
			}
			break
		}
	}
	if pc := javaJSON("POST", "/api/treatments/patient-counts/fetch?tier=Agent", base); pc != nil {
		if m, ok := pc.(map[string]any); ok {
			if t := list(m["patientTreatments"]); len(t) > 0 {
				filters["treatment-patient"] = with("patientTreatmentFilters", map[string]any{"filters": []any{map[string]any{"filters": []any{map[string]any{"treatment": t[0]["treatment"]}}}}})
			}
		}
	}

	for _, fname := range slices.Sorted(maps.Keys(filters)) {
		f := filters[fname]
		n := s + "/sv[" + fname + "]/"
		c.post(n+"filtered-samples", "/api/filtered-samples/fetch", f)
		c.post(n+"clinical-data-counts", "/api/clinical-data-counts/fetch", map[string]any{"attributes": catAttrs, "studyViewFilter": f})
		for _, m := range []string{"STATIC", "DYNAMIC"} {
			c.post(n+"clinical-data-bin-counts-"+m, "/api/clinical-data-bin-counts/fetch?dataBinMethod="+m, map[string]any{"attributes": numAttrs, "studyViewFilter": f})
		}
		c.post(n+"mutated-genes", "/api/mutated-genes/fetch", f)
		c.post(n+"cna-genes", "/api/cna-genes/fetch", f)
		c.post(n+"structuralvariant-genes", "/api/structuralvariant-genes/fetch", f)
		c.post(n+"structuralvariant-counts", "/api/structuralvariant-counts/fetch", f)
		c.post(n+"molecular-profile-sample-counts", "/api/molecular-profile-sample-counts/fetch", f)
		c.post(n+"sample-lists-counts", "/api/sample-lists-counts/fetch", f)
		c.post(n+"clinical-event-type-counts", "/api/clinical-event-type-counts/fetch", f)
		c.post(n+"treatments-patient-counts", "/api/treatments/patient-counts/fetch?tier=Agent", f)
		c.post(n+"treatments-sample-counts", "/api/treatments/sample-counts/fetch?tier=Agent", f)
		c.post(n+"clinical-data-table", "/api/clinical-data-table/fetch?pageSize=20&pageNumber=0&sortBy=SAMPLE_ID&direction=ASC", f)
		c.post(n+"density-plot", "/api/clinical-data-density-plot/fetch?xAxisAttributeId=MUTATION_COUNT&yAxisAttributeId=FRACTION_GENOME_ALTERED&xAxisBinCount=10&yAxisBinCount=10", f)
		c.post(n+"violin", "/api/clinical-data-violin-plots/fetch?categoricalAttributeId=CANCER_TYPE_DETAILED&numericalAttributeId=MUTATION_COUNT", f)
		if mutProfile != "" {
			c.post(n+"mutation-data-counts", "/api/mutation-data-counts/fetch?projection=SUMMARY", map[string]any{"studyViewFilter": f,
				"genomicDataFilters": []any{map[string]any{"hugoGeneSymbol": "TP53", "profileType": strings.TrimPrefix(mutProfile, s+"_")}}})
			c.post(n+"mutation-data-counts-detailed", "/api/mutation-data-counts/fetch?projection=DETAILED", map[string]any{"studyViewFilter": f,
				"genomicDataFilters": []any{map[string]any{"hugoGeneSymbol": "TP53", "profileType": strings.TrimPrefix(mutProfile, s+"_")}}})
		}
		if cnaProfile != "" {
			c.post(n+"genomic-data-counts", "/api/genomic-data-counts/fetch", map[string]any{"studyViewFilter": f,
				"genomicDataFilters": []any{map[string]any{"hugoGeneSymbol": "TP53", "profileType": strings.TrimPrefix(cnaProfile, s+"_")}}})
		}
		// One filter per request, as the study view sends them (Java reads the
		// profile type of the first filter only).
		for _, g := range [][2]string{{"TP53", "mrna_median_Zscores"}, {"ERCC5", "log2CNA"}} {
			c.post(n+"genomic-data-bin-counts:"+g[1], "/api/genomic-data-bin-counts/fetch?dataBinMethod=STATIC", map[string]any{"studyViewFilter": f,
				"genomicDataBinFilters": []any{map[string]any{"hugoGeneSymbol": g[0], "profileType": g[1]}}})
		}
		for _, g := range gaChart {
			if g["kind"] == "num" {
				c.post(n+"ga-bin-counts/"+str(g["profileType"]), "/api/generic-assay-data-bin-counts/fetch?dataBinMethod=STATIC", map[string]any{"studyViewFilter": f,
					"genericAssayDataBinFilters": []any{map[string]any{"stableId": g["stableId"], "profileType": g["profileType"]}}})
			} else {
				c.post(n+"ga-counts/"+str(g["profileType"]), "/api/generic-assay-data-counts/fetch", map[string]any{"studyViewFilter": f,
					"genericAssayDataFilters": []any{map[string]any{"stableId": g["stableId"], "profileType": g["profileType"]}}})
			}
		}
	}
}

func (c *corpus) multiStudy() {
	all := map[string]any{"studyIds": fixtureStudies, "alterationFilter": allAlterations()}
	n := "multi/"
	c.post(n+"filtered-samples", "/api/filtered-samples/fetch", all)
	c.post(n+"clinical-data-counts", "/api/clinical-data-counts/fetch", map[string]any{"studyViewFilter": all,
		"attributes": []any{map[string]any{"attributeId": "CANCER_TYPE"}, map[string]any{"attributeId": "SAMPLE_TYPE"}, map[string]any{"attributeId": "SAMPLE_COUNT"}, map[string]any{"attributeId": "MUTATION_COUNT"}}})
	c.post(n+"clinical-data-bin-counts", "/api/clinical-data-bin-counts/fetch?dataBinMethod=STATIC", map[string]any{"studyViewFilter": all,
		"attributes": []any{map[string]any{"attributeId": "MUTATION_COUNT"}, map[string]any{"attributeId": "FRACTION_GENOME_ALTERED"}, map[string]any{"attributeId": "AGE"}}})
	c.post(n+"mutated-genes", "/api/mutated-genes/fetch", all)
	c.post(n+"cna-genes", "/api/cna-genes/fetch", all)
	c.post(n+"molecular-profile-sample-counts", "/api/molecular-profile-sample-counts/fetch", all)
	c.post(n+"sample-lists-counts", "/api/sample-lists-counts/fetch", all)
	c.post(n+"mutations", "/api/mutations/fetch?projection=SUMMARY", map[string]any{"molecularProfileIds": []string{
		"study_es_0_mutations", "teststudy_genepanels_mutations", "ascn_test_study_mutations"}, "entrezGeneIds": []int{7157, 672}})
}

func firstN[T any](a []T, n int) []T {
	if len(a) > n {
		return a[:n]
	}
	return a
}

func firstNs(a []string, n int) []string { return firstN(a, n) }

func toEntityIDs(idents []map[string]string, key string) []map[string]string {
	out := []map[string]string{}
	for _, x := range idents {
		out = append(out, map[string]string{"studyId": x["studyId"], "entityId": x[key]})
	}
	return out
}

func sampleMolIDs(samples, profiles []string) []map[string]string {
	out := []map[string]string{}
	for _, p := range profiles {
		for _, s := range samples {
			out = append(out, map[string]string{"molecularProfileId": p, "sampleId": s})
		}
	}
	return out
}

func molGroup(name string, samples []string, profile string) map[string]any {
	ids := []map[string]string{}
	for _, s := range samples {
		ids = append(ids, map[string]string{"caseId": s, "molecularProfileId": profile})
	}
	return map[string]any{"name": name, "molecularProfileCaseIdentifiers": ids}
}

func molGroup2(name string, samples []string, p1, p2 string) map[string]any {
	ids := []map[string]string{}
	for _, p := range []string{p1, p2} {
		for _, s := range samples {
			ids = append(ids, map[string]string{"caseId": s, "molecularProfileId": p})
		}
	}
	return map[string]any{"name": name, "molecularProfileCaseIdentifiers": ids}
}

func sampleIdentsOf(study string, samples []string) []map[string]string {
	out := []map[string]string{}
	for _, s := range samples {
		out = append(out, map[string]string{"studyId": study, "sampleId": s})
	}
	return out
}
