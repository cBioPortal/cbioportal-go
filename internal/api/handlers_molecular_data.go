package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

// alterationTypeTable maps a molecular alteration type to its unpivoted matrix
// staging table (long form: study_id, profile_stable_id, hugo_symbol,
// entrez_gene_id, sample_id, value).
func alterationTypeTable(at string) string {
	switch strings.ToUpper(at) {
	case "COPY_NUMBER_ALTERATION":
		return "cna_matrix"
	case "MRNA_EXPRESSION":
		return "mrna_expression_matrix"
	case "METHYLATION":
		return "methylation_matrix"
	case "PROTEIN_LEVEL":
		return "protein_level_matrix"
	case "GENESET_SCORE":
		return "geneset_score_matrix"
	case "GENERIC_ASSAY":
		return "generic_assay_matrix"
	}
	return ""
}

type profileMeta struct {
	StudyID        string `json:"studyId"`
	AlterationType string `json:"alterationType"`
	Datatype       string `json:"datatype"`
	StableID       string `json:"stableId"`
}

func (s *State) profileMeta(mpid string) (profileMeta, bool) {
	rows, _ := cachedQuery[profileMeta](s, fmt.Sprintf(
		"SELECT study_id AS studyId, molecular_alteration_type AS alterationType, datatype, profile_stable_id AS stableId "+
			"FROM molecular_profile WHERE molecular_profile_id = '%s' LIMIT 1", sqlLit(mpid)))
	if len(rows) == 0 {
		return profileMeta{}, false
	}
	return rows[0], true
}

// dataTable resolves the staging table holding a profile's per-cell data,
// accounting for the DISCRETE_LONG copy-number layout.
func (m profileMeta) dataTable() string {
	if strings.EqualFold(m.AlterationType, "COPY_NUMBER_ALTERATION") &&
		strings.EqualFold(m.Datatype, "DISCRETE_LONG") {
		return "cna_discrete_long"
	}
	return alterationTypeTable(m.AlterationType)
}

type molecularDataRow struct {
	SampleID       string `json:"sampleId"`
	PatientID      string `json:"patientId"`
	EntrezGeneID   *int64 `json:"entrezGeneId"`
	HugoGeneSymbol string `json:"hugoGeneSymbol"`
	GeneType       string `json:"geneType"`
	Value          string `json:"value"`
}

// MolecularData is one sample's value of one gene in a molecular profile,
// with the value as the Java mapper reads it (getTruncatedGeneticAlterationValues).
type MolecularData struct {
	MolecularProfileID string
	SampleID           string
	PatientID          string
	EntrezGeneID       *int64
	Gene               Gene
	StudyID            string
	Value              string
	UniqueSampleKey    string
	UniquePatientKey   string
}

// NumericGeneMolecularData is the Java model as Jackson writes it: value is
// a JSON number (BigDecimal), the nested gene is DETAILED-only.
type NumericGeneMolecularData struct {
	UniqueSampleKey    string      `json:"uniqueSampleKey"`
	UniquePatientKey   string      `json:"uniquePatientKey"`
	EntrezGeneID       *int64      `json:"entrezGeneId,omitempty"`
	Gene               *Gene       `json:"gene,omitempty"`
	MolecularProfileID string      `json:"molecularProfileId"`
	SampleID           string      `json:"sampleId"`
	PatientID          string      `json:"patientId"`
	StudyID            string      `json:"studyId"`
	Value              json.Number `json:"value"`
}

// numericMolecularData is MolecularDataController.filterNonNumberMolecularData:
// records whose value is not a number are dropped.
func numericMolecularData(data []MolecularData, projection string) []NumericGeneMolecularData {
	out := make([]NumericGeneMolecularData, 0, len(data))
	for _, d := range data {
		v, ok := javaDecimal(d.Value)
		if !ok {
			continue
		}
		rec := NumericGeneMolecularData{
			UniqueSampleKey: d.UniqueSampleKey, UniquePatientKey: d.UniquePatientKey, EntrezGeneID: d.EntrezGeneID,
			MolecularProfileID: d.MolecularProfileID, SampleID: d.SampleID, PatientID: d.PatientID, StudyID: d.StudyID,
			Value: v,
		}
		if projection == "DETAILED" {
			g := d.Gene
			rec.Gene = &g
		}
		out = append(out, rec)
	}
	return out
}

type molecularDataFilter struct {
	EntrezGeneIDs []int64  `json:"entrezGeneIds"`
	SampleIDs     []string `json:"sampleIds"`
	SampleListID  string   `json:"sampleListId"`
}

func (s *State) molecularDataFetch(w http.ResponseWriter, r *http.Request) {
	var f molecularDataFilter
	readBody(r, &f)
	out, err := s.molecularData(r.PathValue("id"), f)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, numericMolecularData(out, projectionOf(r)))
}

// molecularDataGet serves GET /api/molecular-profiles/{id}/molecular-data
// (sampleListId and entrezGeneId query parameters).
func (s *State) molecularDataGet(w http.ResponseWriter, r *http.Request) {
	f := molecularDataFilter{SampleListID: r.URL.Query().Get("sampleListId")}
	for _, e := range r.URL.Query()["entrezGeneId"] {
		var id int64
		if _, err := fmt.Sscan(e, &id); err == nil {
			f.EntrezGeneIDs = append(f.EntrezGeneIDs, id)
		}
	}
	out, err := s.molecularData(r.PathValue("id"), f)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, numericMolecularData(out, projectionOf(r)))
}

// molecularData returns a profile's per-sample gene values, optionally limited
// to genes and samples (explicit ids or a sample list), sample-major as
// MolecularDataServiceImpl builds them (samples by stable id). Records are
// limited to reference genes, as the mapper joins the gene table.
func (s *State) molecularData(mpid string, f molecularDataFilter) ([]MolecularData, error) {
	meta, ok := s.profileMeta(mpid)
	if !ok {
		return []MolecularData{}, nil
	}
	table := meta.dataTable()
	// Gene-keyed data only: geneset scores and generic assays have their own
	// endpoints.
	if table == "" || table == "geneset_score_matrix" || table == "generic_assay_matrix" {
		return []MolecularData{}, nil
	}
	where := fmt.Sprintf("study_id = '%s' AND profile_stable_id = '%s'", sqlLit(meta.StudyID), sqlLit(meta.StableID))
	if len(f.SampleIDs) > 0 {
		where += fmt.Sprintf(" AND sample_id IN (%s)", inList(f.SampleIDs))
	} else if f.SampleListID != "" {
		where += fmt.Sprintf(" AND sample_id IN (SELECT sample_id FROM sample_list WHERE list_id = '%s')", sqlLit(f.SampleListID))
	}
	rows, err := ch.Query[molecularDataRow](s.CH, fmt.Sprintf(
		"SELECT m.sample_id AS sampleId, s.patient_id AS patientId, m.entrez AS entrezGeneId, "+
			"if(rg.hugo_gene_symbol != '', rg.hugo_gene_symbol, m.hugo_symbol) AS hugoGeneSymbol, rg.type AS geneType, "+
			"%s AS value FROM (%s) m "+
			"LEFT JOIN sample s ON s.study_id = m.study_id AND s.sample_id = m.sample_id "+
			"LEFT JOIN cbioportal_ref.gene rg ON rg.entrez_gene_id = m.entrez "+
			"WHERE m.entrez IS NOT NULL ORDER BY m.sample_id, m.entrez",
		javaTruncatedValueSQL("m.value"), resolvedEntrezSQL(table, s.rawSource(table, geneMatrixCols...), meta, where, f.EntrezGeneIDs)))
	if err != nil {
		return nil, err
	}
	out := make([]MolecularData, len(rows))
	for i, r := range rows {
		out[i] = MolecularData{
			MolecularProfileID: mpid, SampleID: r.SampleID, PatientID: r.PatientID, EntrezGeneID: r.EntrezGeneID,
			Gene:    Gene{EntrezGeneID: r.EntrezGeneID, HugoGeneSymbol: r.HugoGeneSymbol, Type: r.GeneType},
			StudyID: meta.StudyID, Value: r.Value,
			UniqueSampleKey: uniqueKey(meta.StudyID, r.SampleID), UniquePatientKey: uniqueKey(meta.StudyID, r.PatientID),
		}
	}
	return out, nil
}

// geneKeyMapSources are the staging tables whose gene identifiers the
// transform resolves into gene_key_map.
var geneKeyMapSources = map[string]bool{
	"cna_matrix": true, "cna_discrete_long": true, "mrna_expression_matrix": true, "methylation_matrix": true,
}

// resolvedEntrezSQL selects a profile's rows of a staged per-gene table (read
// from src, a source with at least geneMatrixCols) with an `entrez` column
// holding the gene each row is stored under: the importer's resolution
// (entrez id, symbol or unique alias, recorded in gene_key_map) where the
// transform provides it — rows it could not resolve are dropped — and
// otherwise the row's entrez id or its symbol's. entrez, if non-empty,
// restricts the result to those genes.
func resolvedEntrezSQL(table, src string, meta profileMeta, where string, entrez []int64) string {
	ids := make([]string, len(entrez))
	for i, e := range entrez {
		ids[i] = fmt.Sprintf("%d", e)
	}
	idList := strings.Join(ids, ", ")
	if geneKeyMapSources[table] {
		keyFilter := fmt.Sprintf("source = '%s' AND study_id = '%s' AND profile_stable_id = '%s'",
			table, sqlLit(meta.StudyID), sqlLit(meta.StableID))
		if len(entrez) > 0 {
			keyFilter += fmt.Sprintf(" AND resolved_entrez_gene_id IN (%s)", idList)
			// The symbol condition alone is implied by the pair; it lets a
			// matrix sorted by symbol skip the other genes.
			where += fmt.Sprintf(" AND hugo_symbol IN (SELECT hugo_symbol FROM gene_key_map WHERE %[1]s) "+
				"AND (hugo_symbol, entrez_gene_id) IN (SELECT hugo_symbol, entrez_gene_id FROM gene_key_map WHERE %[1]s)", keyFilter)
		}
		return fmt.Sprintf("SELECT t.*, gk.resolved_entrez_gene_id AS entrez FROM (SELECT * FROM %s WHERE %s) t "+
			"INNER JOIN (SELECT * FROM gene_key_map WHERE %s) gk "+
			"ON gk.hugo_symbol = t.hugo_symbol AND gk.entrez_gene_id = t.entrez_gene_id",
			src, where, keyFilter)
	}
	if len(entrez) > 0 {
		where += fmt.Sprintf(" AND (toInt64OrNull(entrez_gene_id) IN (%[1]s) OR (ifNull(toInt64OrNull(entrez_gene_id), 0) <= 0 "+
			"AND upper(hugo_symbol) IN (SELECT upper(hugo_gene_symbol) FROM cbioportal_ref.gene WHERE entrez_gene_id IN (%[1]s))))", idList)
	}
	return fmt.Sprintf("SELECT t.*, if(toInt64OrNull(t.entrez_gene_id) > 0, toInt64OrNull(t.entrez_gene_id), "+
		"nullIf(toInt64(g.entrez_gene_id), 0)) AS entrez FROM (SELECT * FROM %s WHERE %s) t "+
		"LEFT JOIN cbioportal_ref.gene g ON t.hugo_symbol != '' AND upper(t.hugo_symbol) = upper(g.hugo_gene_symbol)",
		src, where)
}
