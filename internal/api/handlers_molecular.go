package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

// ---------- Molecular profiles ----------

type mpRow struct {
	MolecularProfileID      string `json:"molecularProfileId"`
	StudyID                 string `json:"studyId"`
	MolecularAlterationType string `json:"molecularAlterationType"`
	Datatype                string `json:"datatype"`
	Name                    string `json:"name"`
	Description             string `json:"description"`
	ShowFlag                int    `json:"showFlag"`
	GenericAssayType        string `json:"genericAssayType"`
}

type MolecularProfile struct {
	MolecularProfileID       string `json:"molecularProfileId"`
	StudyID                  string `json:"studyId"`
	MolecularAlterationType  string `json:"molecularAlterationType"`
	Datatype                 string `json:"datatype"`
	Name                     string `json:"name"`
	Description              string `json:"description"`
	ShowProfileInAnalysisTab bool   `json:"showProfileInAnalysisTab"`
	PatientLevel             bool   `json:"patientLevel"`
	GenericAssayType         string `json:"genericAssayType,omitempty"`
}

const mpSelect = "SELECT molecular_profile_id AS molecularProfileId, study_id AS studyId, " +
	"molecular_alteration_type AS molecularAlterationType, datatype, name, description, " +
	"toUInt8(show_profile_in_analysis_tab) AS showFlag, generic_assay_type AS genericAssayType FROM molecular_profile"

func toMP(r mpRow) MolecularProfile {
	// DISCRETE_LONG is our internal file-layout marker (routes to cna_discrete_long);
	// the frontend only recognizes DISCRETE as a discrete CNA profile, so present
	// it as DISCRETE in the API. profileMeta reads the view directly and keeps the
	// long form for data-table routing.
	datatype := r.Datatype
	if datatype == "DISCRETE_LONG" {
		datatype = "DISCRETE"
	}
	return MolecularProfile{
		MolecularProfileID: r.MolecularProfileID, StudyID: r.StudyID, MolecularAlterationType: r.MolecularAlterationType,
		Datatype: datatype, Name: r.Name, Description: r.Description, ShowProfileInAnalysisTab: r.ShowFlag != 0,
		GenericAssayType: r.GenericAssayType,
	}
}

func (s *State) mpQuery(w http.ResponseWriter, where string) {
	sql := mpSelect
	if where != "" {
		sql += " WHERE " + where
	}
	rows, err := ch.Query[mpRow](s.CH, sql+" ORDER BY molecularProfileId")
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]MolecularProfile, len(rows))
	for i, r := range rows {
		out[i] = toMP(r)
	}
	writeJSON(w, out)
}

func (s *State) molecularProfiles(w http.ResponseWriter, r *http.Request) { s.mpQuery(w, "") }

func (s *State) studyMolecularProfiles(w http.ResponseWriter, r *http.Request) {
	s.mpQuery(w, fmt.Sprintf("study_id = '%s'", sqlLit(r.PathValue("studyId"))))
}

func (s *State) molecularProfile(w http.ResponseWriter, r *http.Request) {
	sql := fmt.Sprintf("%s WHERE molecular_profile_id = '%s'", mpSelect, sqlLit(r.PathValue("id")))
	rows, err := ch.Query[mpRow](s.CH, sql)
	if err != nil {
		fail(w, err)
		return
	}
	if len(rows) == 0 {
		http.Error(w, "molecular profile not found", http.StatusNotFound)
		return
	}
	writeJSON(w, toMP(rows[0]))
}

func (s *State) molecularProfilesFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		StudyIDs            []string `json:"studyIds"`
		MolecularProfileIDs []string `json:"molecularProfileIds"`
	}
	readBody(r, &f)
	switch {
	case len(f.MolecularProfileIDs) > 0:
		s.mpQuery(w, fmt.Sprintf("molecular_profile_id IN (%s)", inList(f.MolecularProfileIDs)))
	case len(f.StudyIDs) > 0:
		s.mpQuery(w, fmt.Sprintf("study_id IN (%s)", inList(f.StudyIDs)))
	default:
		writeJSON(w, []MolecularProfile{})
	}
}

// ---------- Mutations ----------

const mutationSelect = "SELECT study_id AS studyId, sample_id AS sampleId, hugo_symbol AS hugoGeneSymbol, " +
	"toInt64OrNull(entrez_gene_id) AS entrezGeneId, chromosome AS chr, toInt64OrNull(start_position) AS startPosition, " +
	"toInt64OrNull(end_position) AS endPosition, reference_allele AS referenceAllele, tumor_seq_allele2 AS variantAllele, " +
	"protein_change AS proteinChange, variant_classification AS mutationType, toInt64OrNull(t_alt_count) AS tumorAltCount, " +
	"toInt64OrNull(t_ref_count) AS tumorRefCount, cbp_driver AS driverFilter, cbp_driver_annotation AS driverFilterAnnotation, " +
	"cbp_driver_tiers AS driverTiersFilter, cbp_driver_tiers_annotation AS driverTiersFilterAnnotation, namespace_columns AS namespaceColumns, " +
	"molecular_profile_id AS molecularProfileId, patient_id AS patientId, center, mutation_status AS mutationStatus, " +
	"validation_status AS validationStatus, toInt64OrNull(n_alt_count) AS normalAltCount, toInt64OrNull(n_ref_count) AS normalRefCount, " +
	"amino_acid_change AS aminoAcidChange, ncbi_build AS ncbiBuild, variant_type AS variantType, refseq_mrna_id AS refseqMrnaId, " +
	"protein_position AS proteinPosition, toInt64OrNull(ascn_integer_copy_number) AS ascnIntegerCopyNumber, ascn_method AS ascnMethod, " +
	"toFloat64OrNull(ascn_ccf_expected_copies_upper) AS ccfExpectedCopiesUpper, toFloat64OrNull(ascn_ccf_expected_copies) AS ccfExpectedCopies, " +
	"ascn_clonal AS clonal, toInt64OrNull(ascn_minor_copy_number) AS minorCopyNumber, toInt64OrNull(ascn_expected_alt_copies) AS expectedAltCopies, " +
	"toInt64OrNull(ascn_total_copy_number) AS totalCopyNumber FROM mutation"

type mutationRow struct {
	StudyID         string `json:"studyId"`
	SampleID        string `json:"sampleId"`
	HugoGeneSymbol  string `json:"hugoGeneSymbol"`
	EntrezGeneID    *int64 `json:"entrezGeneId"`
	Chr             string `json:"chr"`
	StartPosition   *int64 `json:"startPosition"`
	EndPosition     *int64 `json:"endPosition"`
	ReferenceAllele string `json:"referenceAllele"`
	VariantAllele   string `json:"variantAllele"`
	ProteinChange   string `json:"proteinChange"`
	MutationType    string `json:"mutationType"`
	TumorAltCount   *int64 `json:"tumorAltCount"`
	TumorRefCount   *int64 `json:"tumorRefCount"`

	DriverFilter                string `json:"driverFilter"`
	DriverFilterAnnotation      string `json:"driverFilterAnnotation"`
	DriverTiersFilter           string `json:"driverTiersFilter"`
	DriverTiersFilterAnnotation string `json:"driverTiersFilterAnnotation"`
	NamespaceColumns            string `json:"namespaceColumns"`

	MolecularProfileID string `json:"molecularProfileId"`
	PatientID          string `json:"patientId"`
	Center             string `json:"center"`
	MutationStatus     string `json:"mutationStatus"`
	ValidationStatus   string `json:"validationStatus"`
	NormalAltCount     *int64 `json:"normalAltCount"`
	NormalRefCount     *int64 `json:"normalRefCount"`
	AminoAcidChange    string `json:"aminoAcidChange"`
	NcbiBuild          string `json:"ncbiBuild"`
	VariantType        string `json:"variantType"`
	RefseqMrnaID       string `json:"refseqMrnaId"`
	ProteinPosition    string `json:"proteinPosition"`
	alleleSpecificCopyNumber
}

type Gene struct {
	EntrezGeneID   *int64 `json:"entrezGeneId"`
	HugoGeneSymbol string `json:"hugoGeneSymbol"`
}

type Mutation struct {
	MolecularProfileID string `json:"molecularProfileId"`
	SampleID           string `json:"sampleId"`
	StudyID            string `json:"studyId"`
	EntrezGeneID       *int64 `json:"entrezGeneId"`
	Gene               Gene   `json:"gene"`
	Chr                string `json:"chr"`
	StartPosition      *int64 `json:"startPosition"`
	EndPosition        *int64 `json:"endPosition"`
	ReferenceAllele    string `json:"referenceAllele"`
	VariantAllele      string `json:"variantAllele"`
	ProteinChange      string `json:"proteinChange"`
	MutationType       string `json:"mutationType"`
	TumorAltCount      *int64 `json:"tumorAltCount"`
	TumorRefCount      *int64 `json:"tumorRefCount"`
	UniqueSampleKey    string `json:"uniqueSampleKey"`

	DriverFilter                string          `json:"driverFilter"`
	DriverFilterAnnotation      string          `json:"driverFilterAnnotation"`
	DriverTiersFilter           string          `json:"driverTiersFilter"`
	DriverTiersFilterAnnotation string          `json:"driverTiersFilterAnnotation"`
	NamespaceColumns            json.RawMessage `json:"namespaceColumns"`

	PatientID                string                    `json:"patientId"`
	UniquePatientKey         string                    `json:"uniquePatientKey"`
	Center                   string                    `json:"center,omitempty"`
	MutationStatus           string                    `json:"mutationStatus,omitempty"`
	ValidationStatus         string                    `json:"validationStatus,omitempty"`
	NormalAltCount           *int64                    `json:"normalAltCount,omitempty"`
	NormalRefCount           *int64                    `json:"normalRefCount,omitempty"`
	AminoAcidChange          string                    `json:"aminoAcidChange,omitempty"`
	NcbiBuild                string                    `json:"ncbiBuild,omitempty"`
	VariantType              string                    `json:"variantType,omitempty"`
	RefseqMrnaID             string                    `json:"refseqMrnaId,omitempty"`
	ProteinPosStart          *int64                    `json:"proteinPosStart,omitempty"`
	ProteinPosEnd            *int64                    `json:"proteinPosEnd,omitempty"`
	AlleleSpecificCopyNumber *alleleSpecificCopyNumber `json:"alleleSpecificCopyNumber,omitempty"`
}

// alleleSpecificCopyNumber is the ASCN namespace of a MAF record.
type alleleSpecificCopyNumber struct {
	AscnIntegerCopyNumber  *int64   `json:"ascnIntegerCopyNumber,omitempty"`
	AscnMethod             string   `json:"ascnMethod,omitempty"`
	CcfExpectedCopiesUpper *float64 `json:"ccfExpectedCopiesUpper,omitempty"`
	CcfExpectedCopies      *float64 `json:"ccfExpectedCopies,omitempty"`
	Clonal                 string   `json:"clonal,omitempty"`
	MinorCopyNumber        *int64   `json:"minorCopyNumber,omitempty"`
	ExpectedAltCopies      *int64   `json:"expectedAltCopies,omitempty"`
	TotalCopyNumber        *int64   `json:"totalCopyNumber,omitempty"`
}

func (a alleleSpecificCopyNumber) present() bool {
	return a.AscnIntegerCopyNumber != nil || a.AscnMethod != "" || a.CcfExpectedCopiesUpper != nil ||
		a.CcfExpectedCopies != nil || a.Clonal != "" || a.MinorCopyNumber != nil || a.ExpectedAltCopies != nil ||
		a.TotalCopyNumber != nil
}

// proteinPositions parses Protein_position ("600/766", "600-601/766"), else
// the first number(s) of the protein change ("V600E" -> 600, "K23_E24del" -> 23-24).
func proteinPositions(position, change string) (*int64, *int64) {
	parse := func(s string) []int64 {
		var out []int64
		var cur int64
		in := false
		for _, c := range s {
			if c >= '0' && c <= '9' {
				cur = cur*10 + int64(c-'0')
				in = true
			} else if in {
				out = append(out, cur)
				cur, in = 0, false
			}
		}
		if in {
			out = append(out, cur)
		}
		return out
	}
	if p, _, _ := strings.Cut(position, "/"); p != "" {
		if n := parse(p); len(n) > 0 {
			start, end := n[0], n[len(n)-1]
			return &start, &end
		}
	}
	n := parse(change)
	if len(n) == 0 {
		return nil, nil
	}
	start, end := n[0], n[0]
	if len(n) > 1 && strings.Contains(change, "_") {
		end = n[1]
	}
	return &start, &end
}

func profileStudy(mpid string) string { return strings.TrimSuffix(mpid, "_mutations") }

func toMutation(r mutationRow) Mutation {
	m := Mutation{
		MolecularProfileID: r.StudyID + "_mutations", SampleID: r.SampleID, StudyID: r.StudyID,
		EntrezGeneID: r.EntrezGeneID, Gene: Gene{EntrezGeneID: r.EntrezGeneID, HugoGeneSymbol: r.HugoGeneSymbol},
		Chr: r.Chr, StartPosition: r.StartPosition, EndPosition: r.EndPosition, ReferenceAllele: r.ReferenceAllele,
		VariantAllele: r.VariantAllele, ProteinChange: r.ProteinChange, MutationType: r.MutationType,
		TumorAltCount: r.TumorAltCount, TumorRefCount: r.TumorRefCount, UniqueSampleKey: uniqueKey(r.StudyID, r.SampleID),
		DriverFilter: r.DriverFilter, DriverFilterAnnotation: r.DriverFilterAnnotation,
		DriverTiersFilter: r.DriverTiersFilter, DriverTiersFilterAnnotation: r.DriverTiersFilterAnnotation,
		NamespaceColumns: rawJSONOrEmpty(r.NamespaceColumns),
		PatientID:        r.PatientID, UniquePatientKey: uniqueKey(r.StudyID, r.PatientID),
		Center: r.Center, MutationStatus: r.MutationStatus, ValidationStatus: r.ValidationStatus,
		NormalAltCount: r.NormalAltCount, NormalRefCount: r.NormalRefCount, AminoAcidChange: r.AminoAcidChange,
		NcbiBuild: r.NcbiBuild, VariantType: r.VariantType, RefseqMrnaID: r.RefseqMrnaID,
	}
	if r.MolecularProfileID != "" {
		m.MolecularProfileID = r.MolecularProfileID
	}
	m.ProteinPosStart, m.ProteinPosEnd = proteinPositions(r.ProteinPosition, r.ProteinChange)
	if r.alleleSpecificCopyNumber.present() {
		a := r.alleleSpecificCopyNumber
		m.AlleleSpecificCopyNumber = &a
	}
	return m
}

// rawJSONOrEmpty returns s as raw JSON when it is valid, else the empty object.
func rawJSONOrEmpty(s string) json.RawMessage {
	if s != "" && json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	return json.RawMessage("{}")
}

func (s *State) mutationsWhere(w http.ResponseWriter, where, order string, limit int) {
	sql := fmt.Sprintf("%s WHERE %s ORDER BY %s", mutationSelect, where, order)
	if limit > 0 {
		sql += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := ch.Query[mutationRow](s.CH, sql)
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]Mutation, len(rows))
	for i, r := range rows {
		out[i] = toMutation(r)
	}
	writeJSON(w, out)
}

// mutations serves GET /api/molecular-profiles/{id}/mutations: the profile's
// mutations in the sampleListId sample list, for the entrezGeneId gene.
func (s *State) mutations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	where := fmt.Sprintf("study_id = '%s'", sqlLit(profileStudy(r.PathValue("id"))))
	where += sampleListClause("sample_id", q.Get("sampleListId"))
	var entrez []int64
	for _, e := range q["entrezGeneId"] {
		if id, err := strconv.ParseInt(e, 10, 64); err == nil {
			entrez = append(entrez, id)
		}
	}
	where += geneFilter(entrez)
	limit := 0
	if q.Get("sampleListId") == "" && len(entrez) == 0 {
		limit = 100000
	}
	s.mutationsWhere(w, where, "sample_id", limit)
}

// sampleListClause restricts col to the members of a sample list (no-op for
// an empty list id).
func sampleListClause(col, listID string) string {
	if listID == "" {
		return ""
	}
	return fmt.Sprintf(" AND %s IN (SELECT sample_id FROM sample_list WHERE list_id = '%s')", col, sqlLit(listID))
}

type mutationFilter struct {
	SampleMolecularIdentifiers []struct {
		MolecularProfileID string `json:"molecularProfileId"`
		SampleID           string `json:"sampleId"`
	} `json:"sampleMolecularIdentifiers"`
	SampleIDs           []string `json:"sampleIds"`
	SampleListID        string   `json:"sampleListId"`
	MolecularProfileIDs []string `json:"molecularProfileIds"`
	EntrezGeneIDs       []int64  `json:"entrezGeneIds"`
}

func geneFilter(entrez []int64) string {
	if len(entrez) == 0 {
		return ""
	}
	ids := make([]string, len(entrez))
	for i, e := range entrez {
		ids[i] = fmt.Sprintf("%d", e)
	}
	return fmt.Sprintf(" AND toInt64OrNull(entrez_gene_id) IN (%s)", strings.Join(ids, ", "))
}

func (s *State) mutationsFetchProfile(w http.ResponseWriter, r *http.Request) {
	var f mutationFilter
	readBody(r, &f)
	study := sqlLit(profileStudy(r.PathValue("id")))
	where := fmt.Sprintf("study_id = '%s'", study)
	// MutationFilter is EITHER explicit sample ids OR a sample list.
	if len(f.SampleIDs) > 0 {
		where += fmt.Sprintf(" AND sample_id IN (%s)", inList(f.SampleIDs))
	} else {
		where += sampleListClause("sample_id", f.SampleListID)
	}
	where += geneFilter(f.EntrezGeneIDs)
	s.mutationsWhere(w, where, "sample_id", 0)
}

func (s *State) mutationsFetch(w http.ResponseWriter, r *http.Request) {
	var f mutationFilter
	readBody(r, &f)
	// MutationMultipleStudyFilter is EITHER a set of (profile, sample) pairs OR a
	// list of molecular profiles (all their samples), each with an optional gene
	// filter. The results view uses the molecularProfileIds form.
	if len(f.SampleMolecularIdentifiers) > 0 {
		pairs := make([][2]string, len(f.SampleMolecularIdentifiers))
		for i, id := range f.SampleMolecularIdentifiers {
			pairs[i] = [2]string{profileStudy(id.MolecularProfileID), id.SampleID}
		}
		where := fmt.Sprintf("(study_id, sample_id) IN (%s)%s", tupleIn(pairs), geneFilter(f.EntrezGeneIDs))
		s.mutationsWhere(w, where, "study_id, sample_id", 0)
		return
	}
	if len(f.MolecularProfileIDs) > 0 {
		studies := make([]string, len(f.MolecularProfileIDs))
		for i, id := range f.MolecularProfileIDs {
			studies[i] = profileStudy(id)
		}
		where := fmt.Sprintf("study_id IN (%s)%s", inList(studies), geneFilter(f.EntrezGeneIDs))
		s.mutationsWhere(w, where, "study_id, sample_id", 0)
		return
	}
	writeJSON(w, []Mutation{})
}

// ---------- Gene panels ----------

type panelGeneRow struct {
	GenePanelID    string `json:"genePanelId"`
	Description    string `json:"description"`
	HugoGeneSymbol string `json:"hugoGeneSymbol"`
	EntrezGeneID   *int64 `json:"entrezGeneId"`
}

type PanelGene struct {
	EntrezGeneID   *int64 `json:"entrezGeneId"`
	HugoGeneSymbol string `json:"hugoGeneSymbol"`
}

type GenePanel struct {
	GenePanelID string      `json:"genePanelId"`
	Description string      `json:"description"`
	Genes       []PanelGene `json:"genes"`
}

// panelSelect lists each panel gene once: panels are global by stable id even
// when several studies ship the same panel file.
const panelSelect = "SELECT genePanelId, description, hugoGeneSymbol, entrezGeneId FROM (" +
	"SELECT gene_panel_id AS genePanelId, any(description) AS description, hugo_gene_symbol AS hugoGeneSymbol, " +
	"any(toInt64OrNull(entrez_gene_id)) AS entrezGeneId FROM gene_panel GROUP BY gene_panel_id, hugo_gene_symbol)"

func groupPanels(rows []panelGeneRow) []GenePanel {
	var out []GenePanel
	for _, r := range rows {
		if len(out) == 0 || out[len(out)-1].GenePanelID != r.GenePanelID {
			out = append(out, GenePanel{GenePanelID: r.GenePanelID, Description: r.Description})
		}
		p := &out[len(out)-1]
		p.Genes = append(p.Genes, PanelGene{EntrezGeneID: r.EntrezGeneID, HugoGeneSymbol: r.HugoGeneSymbol})
	}
	return out
}

func (s *State) genePanels(w http.ResponseWriter, r *http.Request) {
	rows, err := ch.Query[panelGeneRow](s.CH, panelSelect+" ORDER BY genePanelId, hugoGeneSymbol")
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, groupPanels(rows))
}

// genePanelsFetch returns gene panels (with their gene lists) for the posted
// panel IDs (raw JSON array of strings) — used by results/patient views to build
// per-gene coverage.
func (s *State) genePanelsFetch(w http.ResponseWriter, r *http.Request) {
	var ids []string
	readBody(r, &ids)
	if len(ids) == 0 {
		writeJSON(w, []GenePanel{})
		return
	}
	sql := fmt.Sprintf("%s WHERE genePanelId IN (%s) ORDER BY genePanelId, hugoGeneSymbol", panelSelect, inList(ids))
	rows, err := ch.Query[panelGeneRow](s.CH, sql)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, groupPanels(rows))
}

func (s *State) genePanel(w http.ResponseWriter, r *http.Request) {
	sql := fmt.Sprintf("%s WHERE genePanelId = '%s' ORDER BY hugoGeneSymbol", panelSelect, sqlLit(r.PathValue("id")))
	rows, err := ch.Query[panelGeneRow](s.CH, sql)
	if err != nil {
		fail(w, err)
		return
	}
	panels := groupPanels(rows)
	if len(panels) == 0 {
		http.Error(w, "gene panel not found", http.StatusNotFound)
		return
	}
	writeJSON(w, panels[0])
}
