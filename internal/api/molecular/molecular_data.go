package molecular

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

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

// numericRecord is one record of MolecularDataController.filterNonNumberMolecularData:
// ok is false for a value that is not a number, which Java drops.
func numericRecord(d MolecularData, projection string) (NumericGeneMolecularData, bool) {
	v, ok := core.JavaDecimal(d.Value)
	if !ok {
		return NumericGeneMolecularData{}, false
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
	return rec, true
}

type molecularDataFilter struct {
	EntrezGeneIDs []int64  `json:"entrezGeneIds"`
	SampleIDs     []string `json:"sampleIds"`
	SampleListID  string   `json:"sampleListId"`
}

func (s Server) molecularDataFetch(w http.ResponseWriter, r *http.Request) {
	var f molecularDataFilter
	core.ReadBody(r, &f)
	if core.NoSamples(f.SampleIDs, f.SampleListID) {
		core.WriteJSON(w, []NumericGeneMolecularData{})
		return
	}
	s.streamMolecularData(w, r, r.PathValue("id"), f)
}

// molecularDataGet serves GET /api/molecular-profiles/{id}/molecular-data
// (sampleListId and entrezGeneId query parameters).
func (s Server) molecularDataGet(w http.ResponseWriter, r *http.Request) {
	if core.BlankSampleList(r.URL.Query()) {
		core.WriteJSON(w, []NumericGeneMolecularData{})
		return
	}
	f := molecularDataFilter{SampleListID: r.URL.Query().Get("sampleListId"), EntrezGeneIDs: singleEntrezGeneID(r.URL.Query())}
	s.streamMolecularData(w, r, r.PathValue("id"), f)
}

// molecularDataSortable are the sortBy fields of molecular data records
// (MolecularDataSortBy) as SQL expressions over molecularDataRow's columns,
// plus the profile and study of the multi-profile fetch. value sorts as a
// number. A field that is the same in every record leaves the order as it
// is.
var molecularDataSortable = map[string]string{
	"entrezGeneId": "entrezGeneId", "sampleId": "sampleId", "patientId": "patientId",
	"studyId": "", "molecularProfileId": "", "value": "toFloat64OrNull(value)",
}

// streamMolecularData writes a profile's numeric records as they are read:
// a whole profile can be millions of records.
func (s Server) streamMolecularData(w http.ResponseWriter, r *http.Request, mpid string, f molecularDataFilter) {
	projection := core.ProjectionOf(r)
	if projection == "META" {
		args := core.NewArgs()
		sel, _, ok := s.molecularDataSelect(args, mpid, f, "")
		if !ok {
			sel = "SELECT '' AS value WHERE 0"
		}
		sql := "SELECT count() AS n FROM (" + sel + ") WHERE " + core.JavaNumericSQL("value")
		core.WriteMetaCount(w, r, s.CH, sql, args.Params(sql))
		return
	}
	out := core.NewJSONArray(w, r)
	err := s.molecularDataPage(r, mpid, f, molecularDataSortable, core.JavaNumericSQL("value"), func(d MolecularData) error {
		if rec, ok := numericRecord(d, projection); ok {
			return out.Add(rec)
		}
		return nil
	})
	out.Finish(err)
}

// molecularData returns a profile's per-sample gene values, optionally limited
// to genes and samples (explicit ids or a sample list), sample-major as
// MolecularDataServiceImpl builds them: requested samples (ids or a list) by
// stable id, as fetchSamples with the ID projection returns them, all of the
// profile's samples by internal id (getSamplesByInternalIds); within a sample
// in genetic entity id order, the primary key order of Java's
// genetic_alteration rows. Records are limited to reference genes, as the
// mapper joins the gene table.
func (s Server) molecularData(mpid string, f molecularDataFilter) ([]MolecularData, error) {
	out := []MolecularData{}
	err := s.molecularDataEach(mpid, f, func(d MolecularData) error {
		out = append(out, d)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// molecularDataEach calls fn with each record of molecularData, in order,
// without holding them.
func (s Server) molecularDataEach(mpid string, f molecularDataFilter, fn func(MolecularData) error) error {
	return s.molecularDataPage(nil, mpid, f, nil, "", fn)
}

// molecularDataPage is molecularDataEach reading only the page the request
// r asks for, when there is one and its sortBy is among sortable (sortBy ->
// SQL expression over molecularDataRow's columns): the records whose value
// passes keep (a SQL predicate over the value column, the records fn keeps
// of all it is called with) are sorted and cut in SQL, ties in
// molecularDataEach's order. r may be nil.
func (s Server) molecularDataPage(r *http.Request, mpid string, f molecularDataFilter, sortable map[string]string, keep string,
	fn func(MolecularData) error) error {
	var sp *core.SQLPage
	var expr string
	if r != nil {
		sp, expr = core.ClaimListPage(r, sortable)
	}
	sampleOrder := s.SampleOrder("s", "m.sample_id")
	if len(f.SampleIDs) > 0 || f.SampleListID != "" {
		sampleOrder = "m.sample_id"
	}
	extra := ""
	if sp != nil {
		extra = fmt.Sprintf(", tuple(%s) AS pageSampleRank, rg.genetic_entity_id AS pageEntityRank", sampleOrder)
	}
	args := core.NewArgs()
	sel, meta, ok := s.molecularDataSelect(args, mpid, f, extra)
	if !ok {
		return nil
	}
	query := fmt.Sprintf("%s ORDER BY %s, rg.genetic_entity_id, m.entrez", sel, sampleOrder)
	if sp != nil {
		query = "SELECT * FROM (" + sel + ") WHERE " + keep +
			sp.OrderLimit(expr, "pageSampleRank, pageEntityRank, entrezGeneId")
	}
	return ch.QueryEachP(s.CH, query, args.Params(query), func(r *molecularDataRow) error {
		return fn(MolecularData{
			MolecularProfileID: mpid, SampleID: r.SampleID, PatientID: r.PatientID, EntrezGeneID: r.EntrezGeneID,
			Gene:    Gene{EntrezGeneID: r.EntrezGeneID, HugoGeneSymbol: r.HugoGeneSymbol, Type: r.GeneType},
			StudyID: meta.StudyID, Value: r.Value,
			UniqueSampleKey: core.UniqueKey(meta.StudyID, r.SampleID), UniquePatientKey: core.UniqueKey(meta.StudyID, r.PatientID),
		})
	})
}

// molecularDataSelect is the SELECT (without ORDER BY) of a profile's gene
// values for molecularDataEach, the columns of molecularDataRow over the
// aliases m (the values), s (their sample) and rg (their reference gene),
// plus extra columns if any. ok is false for a profile without gene-keyed
// values. Values are bound in args.
func (s Server) molecularDataSelect(args *core.SQLArgs, mpid string, f molecularDataFilter, extra string) (string, core.ProfileMeta, bool) {
	meta, ok := s.ProfileMeta(mpid)
	if !ok {
		return "", meta, false
	}
	table := meta.DataTable()
	// Gene-keyed data only: geneset scores and generic assays have their own
	// endpoints.
	if table == "" || table == "geneset_score_matrix" || table == "generic_assay_matrix" {
		return "", meta, false
	}
	where := core.ByStudy(meta.StudyID).Cond(args, core.RawMatrix(table), "") + " AND profile_stable_id = " + args.Str(meta.StableID)
	if len(f.SampleIDs) > 0 {
		where += " AND sample_id IN " + args.Strs(f.SampleIDs)
	} else if f.SampleListID != "" {
		where += " AND sample_id IN " + s.BySampleList(f.SampleListID).ListMembers(args)
	}
	// Every value row is of the profile's study, so only that study's
	// samples (a key range) are joined; rows resolved through gene_key_map
	// carry one of the requested genes, so only those are joined.
	keyMapped := s.GeneKeyMapped(table)
	genes := "cbioportal_ref.gene"
	if keyMapped && len(f.EntrezGeneIDs) > 0 {
		genes = "(SELECT * FROM cbioportal_ref.gene WHERE entrez_gene_id IN (" + core.IntListSQL(f.EntrezGeneIDs) + "))"
	}
	return fmt.Sprintf(
		"SELECT m.sample_id AS sampleId, s.patient_id AS patientId, m.entrez AS entrezGeneId, "+
			"if(rg.hugo_gene_symbol != '', rg.hugo_gene_symbol, m.hugo_symbol) AS hugoGeneSymbol, rg.type AS geneType, "+
			"%s AS value%s FROM (%s) m "+
			"LEFT JOIN %s s ON s.study_id = m.study_id AND s.sample_id = m.sample_id "+
			"LEFT JOIN %s rg ON rg.entrez_gene_id = m.entrez "+
			"WHERE m.entrez IS NOT NULL",
		core.JavaTruncatedValueSQL("m.value"), extra,
		resolvedEntrezSQL(args, table, keyMapped, s.GeneKeySource(table, geneMatrixCols...), meta, where, f.EntrezGeneIDs),
		core.ByStudy(meta.StudyID).Samples(args), genes), meta, true
}

// geneKeyMapSources are the staging tables whose gene identifiers the
// transform resolves into gene_key_map.
var geneKeyMapSources = map[string]bool{
	"cna_matrix": true, "cna_discrete_long": true, "mrna_expression_matrix": true, "methylation_matrix": true,
	"protein_level_matrix": true,
}

// GeneKeyMapped reports whether gene_key_map resolves the genes of a staging
// table: always for geneKeyMapSources, and for another table when
// gene_key_map holds keys of it.
func (s Server) GeneKeyMapped(table string) bool {
	if geneKeyMapSources[table] {
		return true
	}
	type keyRow struct {
		N uint8 `json:"n"`
	}
	rows, err := core.CachedQueryP[keyRow](s.State, "SELECT toUInt8(1) AS n FROM gene_key_map WHERE source = {source:String} LIMIT 1",
		ch.Params{"source": table})
	return err == nil && len(rows) > 0
}

// resolvedEntrezSQL selects a profile's rows of a staged per-gene table (read
// from src, a source with at least geneMatrixCols) with an `entrez` column
// holding the gene each row is stored under: the importer's resolution
// (entrez id, symbol or unique alias, recorded in gene_key_map) where the
// transform provides it (keyMapped) — rows it could not resolve are dropped
// — and otherwise the row's entrez id or its symbol's. entrez, if non-empty,
// restricts the result to those genes. Values are bound in args.
func resolvedEntrezSQL(args *core.SQLArgs, table string, keyMapped bool, src string, meta core.ProfileMeta, where string, entrez []int64) string {
	ids := make([]string, len(entrez))
	for i, e := range entrez {
		ids[i] = fmt.Sprintf("%d", e)
	}
	idList := strings.Join(ids, ", ")
	if keyMapped {
		keyFilter := fmt.Sprintf("source = '%s' AND study_id = %s AND profile_stable_id = %s",
			table, args.Str(meta.StudyID), args.Str(meta.StableID))
		if len(entrez) > 0 {
			keyFilter += fmt.Sprintf(" AND resolved_entrez_gene_id IN (%s)", idList)
			// Implied by the join on (hugo_symbol, entrez_gene_id); it lets a
			// matrix sorted by symbol skip the other genes.
			where += fmt.Sprintf(" AND hugo_symbol IN (SELECT hugo_symbol FROM gene_key_map WHERE %s)", keyFilter)
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
