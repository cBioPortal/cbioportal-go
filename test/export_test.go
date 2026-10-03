package contract

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// Study export (ExportController, GET /export/study/{studyId}.zip). The
// endpoint exists only with feature.study.export=true, which the e2e portal
// properties leave unset, so the main suite checks that the path is then
// served like any other non-API path. The enabled endpoint is checked
// against an instance started with the flag:
//
//	bin/cbioportal-api --portal-properties=... --feature.study.export=true   # e.g. PORT=18096
//	EXPORT_API=http://localhost:18096 go test ./test/ -run Export
//
// Expected file contents come from the fixture study files and the Java
// exporters' rules (meta keys, comment rows, NA for absent MAF strings, ...).

func exportAPI(t *testing.T) string {
	t.Helper()
	base := os.Getenv("EXPORT_API")
	if base == "" {
		t.Skip("EXPORT_API not set — skipping the enabled study-export contracts")
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Get(base + "/health")
	if err != nil {
		t.Skipf("export api not reachable at %s (%v)", base, err)
	}
	resp.Body.Close()
	return base
}

func TestContractExportDisabled(t *testing.T) {
	requireAPI(t)
	if os.Getenv("EXPORT_API") == apiBase() {
		t.Skip("API is the export-enabled instance")
	}
	resp, _ := rawDo(t, http.MethodGet, "/export/study/study_es_0.zip", "")
	if ct := resp.Header.Get("Content-Type"); ct == "application/zip" || resp.Header.Get("Content-Disposition") != "" {
		t.Fatalf("export served without feature.study.export: %d %s", resp.StatusCode, ct)
	}
}

// exportZip downloads a study export and returns its files by name, in zip
// order.
func exportZip(t *testing.T, base, studyID string) (map[string]string, []string) {
	t.Helper()
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Get(base + "/export/study/" + studyID + ".zip")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	wantEq(t, resp.StatusCode, 200)
	wantEq(t, resp.Header.Get("Content-Type"), "application/zip")
	wantEq(t, resp.Header.Get("Content-Disposition"), `attachment; filename="`+studyID+`.zip"`)
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	files := map[string]string{}
	var names []string
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = string(b)
		names = append(names, f.Name)
	}
	return files, names
}

func lines(s string) []string { return strings.Split(strings.TrimSuffix(s, "\n"), "\n") }

func TestContractExportUnknownStudy(t *testing.T) {
	base := exportAPI(t)
	resp, err := http.Get(base + "/export/study/no_such_study.zip")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	wantEq(t, resp.StatusCode, 404)
	wantEq(t, len(b), 0)
}

func TestContractExportStudyEs0(t *testing.T) {
	base := exportAPI(t)
	files, names := exportZip(t, base, "study_es_0")

	// Exporters run in ExportConfig order; protein level and generic assay
	// profiles are not exported, the README comes last.
	wantEq(t, names[:5], []string{"meta_study.txt", "meta_cancer_type.txt", "data_cancer_type.txt",
		"meta_clinical_patient_attributes.txt", "data_clinical_patient_attributes.txt"})
	wantEq(t, names[len(names)-1], "README.txt")
	for _, n := range names {
		if strings.Contains(n, "generic_assay") || strings.Contains(n, "protein_level") {
			t.Errorf("unexpected %s", n)
		}
	}

	// meta_study.txt: the mapper reads neither add_global_case_list nor tags;
	// groups are stored as an upper-cased HashSet (iteration order SU2C-PI3K,
	// PUBLIC, GDAC).
	wantEq(t, files["meta_study.txt"], "cancer_study_identifier: study_es_0\n"+
		"type_of_cancer: brca-es0\nname: Test study es_0\ndescription: Test study es_0\n"+
		"citation: Cell 2018\npmid: 29625048,29596782,29622463,29617662,29625055,29625050\n"+
		"groups: SU2C-PI3K;PUBLIC;GDAC\nreference_genome: hg19\n")

	// Cancer type and its ancestors, no header row.
	wantEq(t, files["data_cancer_type.txt"], "brca-es0\tBreast Invasive Carcinoma\tHotPink\tbreast\n"+
		"breast\tBreast\tHotPink\ttissue\n")
	wantEq(t, files["meta_cancer_type.txt"], "cancer_study_identifier: study_es_0\ngenetic_alteration_type: CANCER_TYPE\n"+
		"datatype: CANCER_TYPE\ndata_filename: data_cancer_type.txt\n")

	// Clinical samples: the file's order and values; the computed
	// MUTATION_COUNT / FRACTION_GENOME_ALTERED are left out.
	smp := lines(files["data_clinical_sample_attributes.txt"])
	wantEq(t, smp[:7], []string{
		"#Patient Identifier\tSample Identifier\tSubtype",
		"#Patient Identifier\tSample Identifier\tSubtype description",
		"#STRING\tSTRING\tSTRING",
		"#1\t1\t1",
		"PATIENT_ID\tSAMPLE_ID\tSUBTYPE",
		"TCGA-A2-A04P\tTCGA-A2-A04P-01\tbasal-like",
		"TCGA-A1-A0SK\tTCGA-A1-A0SK-01\tbasal-like",
	})

	// Clinical patients: attributes sorted by id (SAMPLE_COUNT left out),
	// patients in sample-file order, missing values empty.
	pat := lines(files["data_clinical_patient_attributes.txt"])
	wantEq(t, pat[4:7], []string{
		"PATIENT_ID\tDFS_MONTHS\tDFS_STATUS\tOS_MONTHS\tOS_STATUS",
		"TCGA-A2-A04P\t\t1:Recurred/Progressed\t\t",
		"TCGA-A1-A0SK\t\t\t31.77\t1:DECEASED",
	})

	// MAF: Java's column set; the gene resolved from the reference (the
	// file has no Entrez ids), absent strings as NA, HGVSp_Short from the
	// stored protein change, absent read counts empty.
	maf := lines(files["data_mutation_extended_maf_mutations.txt"])
	wantEq(t, strings.Split(maf[0], "\t")[:3], []string{"Hugo_Symbol", "Entrez_Gene_Id", "Center"})
	wantEq(t, len(strings.Split(maf[0], "\t")), 37)
	wantEq(t, maf[1], "OR11H1\t81061\tgenome.wustl.edu\tGRCh37\t22\t16449539\t16449539\t-1\tMissense_Mutation\tSNP\t"+
		"A\tA\tG\tNA\tNA\tTCGA-A1-A0SB-01\tTCGA-A1-A0SB-10\tA\tA\tNA\tNA\tNA\tNA\tUnknown\tUntested\tSomatic\t"+
		"Phase_IV\tWXS\tnone\t1\tdbGAP\tIllumina GAIIx\tp.V89A\t\t\t\t")
	wantEq(t, files["meta_mutation_extended_maf_mutations.txt"], "cancer_study_identifier: study_es_0\n"+
		"genetic_alteration_type: MUTATION_EXTENDED\ndatatype: MAF\nstable_id: mutations\n"+
		"show_profile_in_analysis_tab: true\nprofile_name: Mutations\n"+
		"profile_description: Mutation data from whole exome sequencing.\npatient_level: false\n"+
		"data_filename: data_mutation_extended_maf_mutations.txt\n")

	// The discrete-long CNA profile is stored (and exported) as DISCRETE;
	// ACAP3 is amplified (2) in TCGA-A2-A04U-01.
	cna := lines(files["data_copy_number_alteration_discrete_gistic.txt"])
	header := strings.Split(cna[0], "\t")
	wantEq(t, header[:3], []string{"Hugo_Symbol", "Entrez_Gene_Id", "TCGA-A1-A0SB-01"})
	col := -1
	for i, h := range header {
		if h == "TCGA-A2-A04U-01" {
			col = i
		}
	}
	for _, l := range cna[1:] {
		if f := strings.Split(l, "\t"); f[0] == "ACAP3" {
			wantEq(t, f[1], "116983")
			wantEq(t, f[col], "2")
		}
	}
	if !strings.Contains(files["meta_copy_number_alteration_discrete_gistic.txt"], "datatype: DISCRETE\n") {
		t.Errorf("gistic meta: %s", files["meta_copy_number_alteration_discrete_gistic.txt"])
	}

	// Segments as stored: integers and Java doubles.
	seg := lines(files["data_copy_number_alteration_seg.txt"])
	wantEq(t, seg[:3], []string{"ID\tchrom\tloc.start\tloc.end\tnum.mark\tseg.mean",
		"TCGA-A2-A04P-01\t1\t3218610\t95674710\t53225\t0.0055",
		"TCGA-A2-A04P-01\t1\t95676511\t95676518\t2\t-1.6636"})
	wantEq(t, files["meta_copy_number_alteration_seg.txt"], "cancer_study_identifier: study_es_0\n"+
		"genetic_alteration_type: COPY_NUMBER_ALTERATION\ndatatype: SEG\n"+
		"description: Copy number alteration segments\nreference_genome_id: hg19\n"+
		"data_filename: data_copy_number_alteration_seg.txt\n")

	// Gene panel matrix: one column per profile with a panel (the SV
	// profile's comes from its meta file's gene_panel), NA where none.
	gpm := lines(files["data_gene_panel_matrix_gene_panel_matrix.txt"])
	wantEq(t, gpm[0], "SAMPLE_ID\tgistic\tmutations\tstructural_variants")
	found := false
	for _, l := range gpm {
		if strings.HasPrefix(l, "TCGA-A1-A0SB-01\t") {
			found = true
			wantEq(t, l, "TCGA-A1-A0SB-01\tTESTPANEL1\tTESTPANEL1\tTESTPANEL1")
		}
	}
	if !found {
		t.Error("TCGA-A1-A0SB-01 missing from the gene panel matrix")
	}

	// Case lists: sorted sample ids.
	seq := lines(files["case_lists/cases_sequenced.txt"])
	wantEq(t, seq[:2], []string{"cancer_study_identifier: study_es_0", "stable_id: study_es_0_sequenced"})
	ids := strings.Split(strings.TrimPrefix(seq[4], "case_list_ids: "), "\t")
	wantEq(t, len(ids), 31)
	wantEq(t, ids[:3], []string{"TCGA-A1-A0SB-01", "TCGA-A1-A0SB-02", "TCGA-A1-A0SK-01"})
	if _, ok := files["case_lists/cases_testprofiled.txt"]; !ok {
		t.Error("case list file named after the stable id missing")
	}

	wantEq(t, files["README.txt"], "This is a README file for the study study_es_0.\n"+
		"This study export may not include all data types available in the study, as export is implemented only for certain data types.\n"+
		"Refer to the documentation for details on the data files included in this export.\n")
}

// Timeline: one file pair per event type, events in file order, the event
// type's data columns sorted.
func TestContractExportTimeline(t *testing.T) {
	base := exportAPI(t)
	files, _ := exportZip(t, base, "lgg_ucsf_2014_test_generic_assay")
	wantEq(t, files["meta_clinical_timeline_SPECIMEN.txt"], "cancer_study_identifier: lgg_ucsf_2014_test_generic_assay\n"+
		"genetic_alteration_type: CLINICAL\ndatatype: TIMELINE\ndata_filename: data_clinical_timeline_SPECIMEN.txt\n")
	wantEq(t, lines(files["data_clinical_timeline_SPECIMEN.txt"])[:3], []string{
		"PATIENT_ID\tSTART_DATE\tSTOP_DATE\tEVENT_TYPE\tSAMPLE_ID\tSURGERY",
		"P01\t124\t\tSPECIMEN\tP01_Pri\tAstro II Initial",
		"P02\t0\t\tSPECIMEN\tP02_Pri\tOA II Initial",
	})
	wantEq(t, lines(files["data_clinical_timeline_TREATMENT.txt"])[:3], []string{
		"PATIENT_ID\tSTART_DATE\tSTOP_DATE\tEVENT_TYPE\tAGENT\tSUBTYPE\tTREATMENT_TYPE",
		"P01\t611\t977\tTREATMENT\tTMZ\tChemotherapy\tMedical Therapy",
		"P01\t977\t1007\tTREATMENT\tTMZ + accutane\t\tMedical Therapy",
	})
	wantEq(t, lines(files["meta_study.txt"])[6], "groups: PUBLIC")
}
