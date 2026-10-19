package molecular

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// ---------- Molecular profiles ----------

type mpRow struct {
	MolecularProfileID      string   `json:"molecularProfileId"`
	StudyID                 string   `json:"studyId"`
	MolecularAlterationType string   `json:"molecularAlterationType"`
	Datatype                string   `json:"datatype"`
	Name                    string   `json:"name"`
	Description             string   `json:"description"`
	ShowFlag                int      `json:"showFlag"`
	GenericAssayType        string   `json:"genericAssayType"`
	PivotThreshold          *float64 `json:"pivotThreshold"`
	SortOrder               *string  `json:"sortOrder"`
	PatientLevel            int      `json:"patientLevel"`
}

type MolecularProfile struct {
	MolecularProfileID       string   `json:"molecularProfileId"`
	StudyID                  string   `json:"studyId"`
	MolecularAlterationType  string   `json:"molecularAlterationType"`
	GenericAssayType         string   `json:"genericAssayType,omitempty"`
	Datatype                 string   `json:"datatype"`
	Name                     string   `json:"name"`
	Description              string   `json:"description"`
	ShowProfileInAnalysisTab bool     `json:"showProfileInAnalysisTab"`
	PivotThreshold           *float64 `json:"pivotThreshold,omitempty"`
	SortOrder                *string  `json:"sortOrder,omitempty"`
	PatientLevel             bool     `json:"patientLevel"`
}

const mpSelectTmpl = "SELECT molecular_profile_id AS molecularProfileId, study_id AS studyId, " +
	"molecular_alteration_type AS molecularAlterationType, datatype, name, description, " +
	"toUInt8(show_profile_in_analysis_tab) AS showFlag, generic_assay_type AS genericAssayType{extra} FROM molecular_profile"

// mpSelect reads the molecular profiles, with the generic-assay pivot
// threshold (a Java Float, so its shortest Float32 text), sort order and
// patient level where the portal database carries them.
func (s Server) mpSelect() string {
	extra := ""
	if s.ModelHasColumn("molecular_profile", "sort_order") {
		extra = ", toFloat64OrNull(toString(toFloat32(pivot_threshold))) AS pivotThreshold, sort_order AS sortOrder, toUInt8(patient_level) AS patientLevel"
	}
	return strings.Replace(mpSelectTmpl, "{extra}", extra, 1)
}

func toMP(r mpRow) MolecularProfile {
	// DISCRETE_LONG is our internal file-layout marker (routes to cna_discrete_long);
	// the frontend only recognizes DISCRETE as a discrete CNA profile, so present
	// it as DISCRETE in the API. ProfileMeta reads the view directly and keeps the
	// long form for data-table routing.
	datatype := r.Datatype
	if datatype == "DISCRETE_LONG" {
		datatype = "DISCRETE"
	}
	return MolecularProfile{
		MolecularProfileID: r.MolecularProfileID, StudyID: r.StudyID, MolecularAlterationType: r.MolecularAlterationType,
		Datatype: datatype, Name: r.Name, Description: r.Description, ShowProfileInAnalysisTab: r.ShowFlag != 0,
		GenericAssayType: r.GenericAssayType, PivotThreshold: r.PivotThreshold, SortOrder: r.SortOrder,
		PatientLevel: r.PatientLevel != 0,
	}
}

// mpQuery writes the molecular profiles matching where, whose placeholders
// take their values from p.
func (s Server) mpQuery(w http.ResponseWriter, r *http.Request, where string, p ch.Params) {
	sql := s.mpSelect()
	if where != "" {
		sql += " WHERE " + where
	}
	rows, err := ch.QueryP[mpRow](s.CH, sql+" ORDER BY molecularProfileId", p)
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	out := make([]MolecularProfile, len(rows))
	for i, r := range rows {
		out[i] = toMP(r)
	}
	core.WriteJSON(w, out)
}

func (s Server) molecularProfiles(w http.ResponseWriter, r *http.Request) { s.mpQuery(w, r, "", nil) }

func (s Server) studyMolecularProfiles(w http.ResponseWriter, r *http.Request) {
	s.mpQuery(w, r, "study_id = {study:String}", ch.Params{"study": r.PathValue("studyId")})
}

func (s Server) molecularProfile(w http.ResponseWriter, r *http.Request) {
	rows, err := ch.QueryP[mpRow](s.CH, s.mpSelect()+" WHERE molecular_profile_id = {id:String}", ch.Params{"id": r.PathValue("id")})
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	if len(rows) == 0 {
		http.Error(w, "molecular profile not found", http.StatusNotFound)
		return
	}
	core.WriteJSON(w, toMP(rows[0]))
}

func (s Server) molecularProfilesFetch(w http.ResponseWriter, r *http.Request) {
	var f struct {
		StudyIDs            []string `json:"studyIds"`
		MolecularProfileIDs []string `json:"molecularProfileIds"`
	}
	core.ReadBody(r, &f)
	switch {
	case len(f.MolecularProfileIDs) > 0:
		s.mpQuery(w, r, "molecular_profile_id IN {ids:Array(String)}", ch.Params{"ids": f.MolecularProfileIDs})
	case len(f.StudyIDs) > 0:
		s.mpQuery(w, r, "study_id IN {ids:Array(String)}", ch.Params{"ids": f.StudyIDs})
	default:
		core.WriteJSON(w, []MolecularProfile{})
	}
}

// ---------- Mutations ----------

// mutationSelect reads the mutation view with each record's reference gene
// (by entrez id): Java reports the gene's Hugo symbol and type.
const mutationSelect = "SELECT study_id AS studyId, sample_id AS sampleId, hugo_symbol AS hugoGeneSymbol, " +
	"rg.rg_hugo AS refHugoGeneSymbol, rg.rg_type AS geneType, " +
	"toInt64OrNull(entrez_gene_id) AS entrezGeneId, chromosome AS chr, toInt64OrNull(start_position) AS startPosition, " +
	"toInt64OrNull(end_position) AS endPosition, reference_allele AS referenceAllele, tumor_seq_allele2 AS variantAllele, " +
	"protein_change AS proteinChange, variant_classification AS mutationType, t_alt_count AS tumorAltCount, " +
	"t_ref_count AS tumorRefCount, cbp_driver AS driverFilter, cbp_driver_annotation AS driverFilterAnnotation, " +
	"cbp_driver_tiers AS driverTiersFilter, cbp_driver_tiers_annotation AS driverTiersFilterAnnotation, namespace_columns AS namespaceColumns, " +
	"molecular_profile_id AS molecularProfileId, patient_id AS patientId, center, mutation_status AS mutationStatus, " +
	"validation_status AS validationStatus, n_alt_count AS normalAltCount, n_ref_count AS normalRefCount, " +
	"ncbi_build AS ncbiBuild, variant_type AS variantType, refseq_mrna_id AS refseqMrnaId, " +
	"protein_position AS proteinPosition, toInt64OrNull(ascn_integer_copy_number) AS ascnIntegerCopyNumber, ascn_method AS ascnMethod, " +
	"toFloat64OrNull(ascn_ccf_expected_copies_upper) AS ccfExpectedCopiesUpper, toFloat64OrNull(ascn_ccf_expected_copies) AS ccfExpectedCopies, " +
	"ascn_clonal AS clonal, toInt64OrNull(ascn_minor_copy_number) AS minorCopyNumber, toInt64OrNull(ascn_expected_alt_copies) AS expectedAltCopies, " +
	"toInt64OrNull(ascn_total_copy_number) AS totalCopyNumber FROM mutation " +
	"LEFT JOIN (SELECT entrez_gene_id AS rg_entrez, hugo_gene_symbol AS rg_hugo, type AS rg_type FROM cbioportal_ref.gene{genes}) rg " +
	"ON rg.rg_entrez = toInt64OrNull(mutation.entrez_gene_id)"

// mutationSelectFor is mutationSelect for records of the given genes (all
// genes when none): only those genes' reference rows are joined.
func mutationSelectFor(entrez []int64) string {
	genes := ""
	if len(entrez) > 0 {
		genes = " WHERE entrez_gene_id IN (" + core.IntListSQL(entrez) + ")"
	}
	return strings.Replace(mutationSelect, "{genes}", genes, 1)
}

// mutationOrder is MutationMapper's default ORDER BY.
const mutationOrder = "molecular_profile_id, sample_id, toInt64OrNull(entrez_gene_id), protein_change"

// mutationSortable are the MutationSortBy columns as SQL over
// mutationSelect's columns computing the record's field (toMutation): MAF
// strings read empty as NA, counts as JavaCount. aminoAcidChange, which
// Java's records leave null, orders nothing. keyword and the protein
// positions are derived in Go, so their pages are sorted by PageList.
var mutationSortable = map[string]string{
	"entrezGeneId":     "entrezGeneId",
	"center":           naSQL("center"),
	"mutationStatus":   naSQL("mutationStatus"),
	"validationStatus": naSQL("validationStatus"),
	"tumorAltCount":    javaCountSQL("tumorAltCount"),
	"tumorRefCount":    javaCountSQL("tumorRefCount"),
	"normalAltCount":   javaCountSQL("normalAltCount"),
	"normalRefCount":   javaCountSQL("normalRefCount"),
	"aminoAcidChange":  "",
	"startPosition":    "startPosition",
	"endPosition":      "endPosition",
	"referenceAllele":  naSQL("referenceAllele"),
	"variantAllele":    "variantAllele",
	"proteinChange":    "proteinChange",
	"mutationType":     naSQL("mutationType"),
	"ncbiBuild":        naSQL("ncbiBuild"),
	"variantType":      naSQL("variantType"),
	"refseqMrnaId":     naSQL("refseqMrnaId"),
}

// naSQL is core.NAString in SQL.
func naSQL(col string) string { return fmt.Sprintf("if(%s = '', 'NA', %s)", col, col) }

// javaCountSQL is JavaCount in SQL: the cell as a number, NaN as 0, clamped
// to the Java int range and truncated; null when it is no number or -1.
func javaCountSQL(col string) string {
	f := fmt.Sprintf("toFloat64OrNull(trimBoth(%s))", col)
	return fmt.Sprintf("nullIf(multiIf(isNull(%[1]s), -1, isNaN(%[1]s), 0, %[1]s >= 2147483647, 2147483647, "+
		"%[1]s <= -2147483648, -2147483648, toInt64(%[1]s)), -1)", f)
}

type mutationRow struct {
	StudyID           string `json:"studyId"`
	SampleID          string `json:"sampleId"`
	HugoGeneSymbol    string `json:"hugoGeneSymbol"`
	RefHugoGeneSymbol string `json:"refHugoGeneSymbol"`
	GeneType          string `json:"geneType"`
	EntrezGeneID      *int64 `json:"entrezGeneId"`
	Chr               string `json:"chr"`
	StartPosition     *int64 `json:"startPosition"`
	EndPosition       *int64 `json:"endPosition"`
	ReferenceAllele   string `json:"referenceAllele"`
	VariantAllele     string `json:"variantAllele"`
	ProteinChange     string `json:"proteinChange"`
	MutationType      string `json:"mutationType"`
	TumorAltCount     string `json:"tumorAltCount"`
	TumorRefCount     string `json:"tumorRefCount"`

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
	NormalAltCount     string `json:"normalAltCount"`
	NormalRefCount     string `json:"normalRefCount"`
	NcbiBuild          string `json:"ncbiBuild"`
	VariantType        string `json:"variantType"`
	RefseqMrnaID       string `json:"refseqMrnaId"`
	ProteinPosition    string `json:"proteinPosition"`
	alleleSpecificCopyNumber
}

// Gene is the nested gene of DETAILED records (GeneMapper.select).
type Gene struct {
	EntrezGeneID   *int64 `json:"entrezGeneId,omitempty"`
	HugoGeneSymbol string `json:"hugoGeneSymbol"`
	Type           string `json:"type,omitempty"`
}

// Mutation is the Java Mutation model as Jackson writes it (NON_NULL): null
// fields are absent. The nested gene and allele-specific copy number belong
// to the DETAILED projection only.
type Mutation struct {
	UniqueSampleKey             string                    `json:"uniqueSampleKey"`
	UniquePatientKey            string                    `json:"uniquePatientKey"`
	MolecularProfileID          string                    `json:"molecularProfileId"`
	SampleID                    string                    `json:"sampleId"`
	PatientID                   string                    `json:"patientId"`
	EntrezGeneID                *int64                    `json:"entrezGeneId,omitempty"`
	Gene                        *Gene                     `json:"gene,omitempty"`
	StudyID                     string                    `json:"studyId"`
	DriverFilter                *string                   `json:"driverFilter,omitempty"`
	DriverFilterAnnotation      *string                   `json:"driverFilterAnnotation,omitempty"`
	DriverTiersFilter           *string                   `json:"driverTiersFilter,omitempty"`
	DriverTiersFilterAnnotation *string                   `json:"driverTiersFilterAnnotation,omitempty"`
	Center                      string                    `json:"center"`
	MutationStatus              string                    `json:"mutationStatus"`
	ValidationStatus            string                    `json:"validationStatus"`
	TumorAltCount               *int64                    `json:"tumorAltCount,omitempty"`
	TumorRefCount               *int64                    `json:"tumorRefCount,omitempty"`
	NormalAltCount              *int64                    `json:"normalAltCount,omitempty"`
	NormalRefCount              *int64                    `json:"normalRefCount,omitempty"`
	Chr                         string                    `json:"chr"`
	StartPosition               *int64                    `json:"startPosition,omitempty"`
	EndPosition                 *int64                    `json:"endPosition,omitempty"`
	ReferenceAllele             string                    `json:"referenceAllele"`
	VariantAllele               string                    `json:"variantAllele"`
	ProteinChange               string                    `json:"proteinChange"`
	MutationType                string                    `json:"mutationType"`
	NcbiBuild                   string                    `json:"ncbiBuild"`
	VariantType                 string                    `json:"variantType"`
	RefseqMrnaID                string                    `json:"refseqMrnaId"`
	ProteinPosStart             int64                     `json:"proteinPosStart"`
	ProteinPosEnd               int64                     `json:"proteinPosEnd"`
	Keyword                     string                    `json:"keyword,omitempty"`
	AlleleSpecificCopyNumber    *alleleSpecificCopyNumber `json:"alleleSpecificCopyNumber,omitempty"`
	NamespaceColumns            json.RawMessage           `json:"namespaceColumns,omitempty"`
}

// MutationID is a mutation in the ID projection.
type MutationID struct {
	UniqueSampleKey    string `json:"uniqueSampleKey"`
	UniquePatientKey   string `json:"uniquePatientKey"`
	MolecularProfileID string `json:"molecularProfileId"`
	SampleID           string `json:"sampleId"`
	PatientID          string `json:"patientId"`
	EntrezGeneID       *int64 `json:"entrezGeneId,omitempty"`
	StudyID            string `json:"studyId"`
}

// alleleSpecificCopyNumber is the ASCN namespace of a MAF record.
type alleleSpecificCopyNumber struct {
	AscnIntegerCopyNumber  *int64          `json:"ascnIntegerCopyNumber,omitempty"`
	AscnMethod             string          `json:"ascnMethod,omitempty"`
	CcfExpectedCopiesUpper *core.JavaFloat `json:"ccfExpectedCopiesUpper,omitempty"`
	CcfExpectedCopies      *core.JavaFloat `json:"ccfExpectedCopies,omitempty"`
	Clonal                 string          `json:"clonal,omitempty"`
	MinorCopyNumber        *int64          `json:"minorCopyNumber,omitempty"`
	ExpectedAltCopies      *int64          `json:"expectedAltCopies,omitempty"`
	TotalCopyNumber        *int64          `json:"totalCopyNumber,omitempty"`
}

func (a alleleSpecificCopyNumber) present() bool {
	return a.AscnIntegerCopyNumber != nil || a.AscnMethod != "" || a.CcfExpectedCopiesUpper != nil ||
		a.CcfExpectedCopies != nil || a.Clonal != "" || a.MinorCopyNumber != nil || a.ExpectedAltCopies != nil ||
		a.TotalCopyNumber != nil
}

// profileStudy is the study of a molecular profile.
func (s Server) profileStudy(mpid string) string { return s.profileStudies([]string{mpid})[mpid] }

// profileStudies maps molecular profile ids to their studies, with one
// (cached) query. A profile id not in the portal falls back to the
// <study>_mutations convention; it matches no rows.
func (s Server) profileStudies(ids []string) map[string]string {
	distinct := slices.Compact(slices.Sorted(slices.Values(ids)))
	rows, _ := core.CachedQueryP[struct {
		ID    string `json:"id"`
		Study string `json:"study"`
	}](s.State, "SELECT molecular_profile_id AS id, study_id AS study FROM molecular_profile "+
		"WHERE molecular_profile_id IN {ids:Array(String)}", ch.Params{"ids": distinct})
	out := make(map[string]string, len(distinct))
	for _, id := range distinct {
		out[id] = strings.TrimSuffix(id, "_mutations")
	}
	for _, r := range rows {
		out[r.ID] = r.Study
	}
	return out
}

// geneHugo is the gene symbol Java reports: the reference gene's, else the
// record's own.
func geneHugo(ref, own string) string {
	if ref != "" {
		return ref
	}
	return own
}

// toMutation maps a mutation view row to the Java Mutation record of the
// SUMMARY / DETAILED projection: string MAF columns read as the importer
// stores them (empty -> "NA"), counts null when absent, protein positions
// and keyword derived as the importer derives them.
func toMutation(r mutationRow, detailed bool) Mutation {
	hugo := geneHugo(r.RefHugoGeneSymbol, r.HugoGeneSymbol)
	mutationType := core.NAString(r.MutationType)
	m := Mutation{
		MolecularProfileID: r.MolecularProfileID, SampleID: r.SampleID, StudyID: r.StudyID,
		EntrezGeneID: r.EntrezGeneID,
		Chr:          core.NAString(r.Chr), StartPosition: r.StartPosition, EndPosition: r.EndPosition,
		ReferenceAllele: core.NAString(r.ReferenceAllele), VariantAllele: r.VariantAllele, ProteinChange: r.ProteinChange,
		MutationType: mutationType, TumorAltCount: core.JavaCount(r.TumorAltCount), TumorRefCount: core.JavaCount(r.TumorRefCount),
		UniqueSampleKey:  core.UniqueKey(r.StudyID, r.SampleID),
		Keyword:          core.MutationKeyword(hugo, r.ProteinChange, mutationType),
		NamespaceColumns: core.NamespaceColumnsJSON(r.NamespaceColumns),
		PatientID:        r.PatientID, UniquePatientKey: core.UniqueKey(r.StudyID, r.PatientID),
		Center: core.NAString(r.Center), MutationStatus: core.NAString(r.MutationStatus), ValidationStatus: core.NAString(r.ValidationStatus),
		NormalAltCount: core.JavaCount(r.NormalAltCount), NormalRefCount: core.JavaCount(r.NormalRefCount),
		NcbiBuild: core.NAString(r.NcbiBuild), VariantType: core.NAString(r.VariantType), RefseqMrnaID: core.NAString(r.RefseqMrnaID),
	}
	d := core.NewDriverAnnotation(r.DriverFilter, r.DriverFilterAnnotation, r.DriverTiersFilter, r.DriverTiersFilterAnnotation, core.MAFDriverFilter)
	m.DriverFilter, m.DriverTiersFilter = d.Filter, d.TiersFilter
	if d.Filter != nil {
		ann, tiersAnn := strings.TrimSpace(r.DriverFilterAnnotation), strings.TrimSpace(r.DriverTiersFilterAnnotation)
		m.DriverFilterAnnotation, m.DriverTiersFilterAnnotation = &ann, &tiersAnn
	}
	if r.MolecularProfileID != "" {
		m.MolecularProfileID = r.MolecularProfileID
	}
	m.ProteinPosStart, m.ProteinPosEnd = core.JavaProteinPositions(r.ProteinPosition, r.ProteinChange)
	if detailed {
		m.Gene = &Gene{EntrezGeneID: r.EntrezGeneID, HugoGeneSymbol: hugo, Type: r.GeneType}
		if r.alleleSpecificCopyNumber.present() {
			a := r.alleleSpecificCopyNumber
			m.AlleleSpecificCopyNumber = &a
		}
	}
	return m
}

// mutationsWhere streams the mutations matching where, whose placeholders
// are bound in args, and which holds the records to the entrez genes (all
// genes when none).
func (s Server) mutationsWhere(w http.ResponseWriter, r *http.Request, where string, args *core.SQLArgs, entrez []int64, limit int) {
	sel := mutationSelectFor(entrez)
	if core.ProjectionOf(r) == "META" && limit == 0 {
		// MutationMeta: the records and their distinct samples.
		sql := fmt.Sprintf("SELECT count() AS n, uniqExact(studyId, sampleId) AS samples FROM (%s WHERE %s)", sel, where)
		rows, err := ch.QueryP[struct {
			N       uint64 `json:"n"`
			Samples uint64 `json:"samples"`
		}](s.CH, sql, args.Params(sql))
		if err != nil {
			core.Fail(w, r, err)
			return
		}
		w.Header().Set("total-count", fmt.Sprint(rows[0].N))
		w.Header().Set("sample-count", fmt.Sprint(rows[0].Samples))
		w.WriteHeader(http.StatusOK)
		return
	}
	sql := fmt.Sprintf("%s WHERE %s ORDER BY %s", sel, where, mutationOrder)
	if limit > 0 {
		sql += fmt.Sprintf(" LIMIT %d", limit)
	} else {
		// mutationOrder leaves ties (records of one gene and protein change in
		// a sample).
		sql = core.SQLStablePageQuery(r, mutationSortable, sel+" WHERE "+where, mutationOrder)
	}
	projection := core.ProjectionOf(r)
	// An ID page sorted by a field SQL does not compute (keyword, the
	// protein positions) is sorted here, by the full records.
	var keyed *core.KeyedPage
	if projection == "ID" {
		keyed = core.ClaimKeyedPage(r)
	}
	out := core.NewJSONArray(w, r)
	err := ch.QueryEachP(s.CH, sql, args.Params(sql), func(row *mutationRow) error {
		if projection == "ID" {
			m := toMutation(*row, false)
			id := MutationID{MolecularProfileID: m.MolecularProfileID, SampleID: m.SampleID, PatientID: m.PatientID,
				EntrezGeneID: m.EntrezGeneID, StudyID: m.StudyID, UniqueSampleKey: m.UniqueSampleKey, UniquePatientKey: m.UniquePatientKey}
			if keyed != nil {
				return keyed.Add(m, id)
			}
			return out.Add(id)
		}
		return out.Add(toMutation(*row, projection == "DETAILED"))
	})
	if keyed != nil && err == nil {
		for _, row := range keyed.Rows() {
			if err = out.Add(row); err != nil {
				break
			}
		}
	}
	out.Finish(err)
}

// mutations serves GET /api/molecular-profiles/{id}/mutations: the profile's
// mutations in the sampleListId sample list, for the entrezGeneId gene.
func (s Server) mutations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if core.BlankSampleList(q) {
		core.WriteJSON(w, []Mutation{})
		return
	}
	args := core.NewArgs()
	where := s.profileMutationsClause(args, r.PathValue("id"))
	where += s.sampleListClause(args, "sample_id", q.Get("sampleListId"), r.PathValue("id"))
	entrez := singleEntrezGeneID(q)
	where += geneFilter(entrez)
	limit := 0
	if q.Get("sampleListId") == "" && len(entrez) == 0 {
		limit = 100000
	}
	s.mutationsWhere(w, r, where, args, entrez, limit)
}

// singleEntrezGeneID is the entrezGeneId query parameter of the GET
// endpoints that take one gene (Integer entrezGeneId): Spring binds the first
// of repeated values.
func singleEntrezGeneID(q url.Values) []int64 {
	if id, err := strconv.ParseInt(strings.TrimSpace(q.Get("entrezGeneId")), 10, 64); err == nil {
		return []int64{id}
	}
	return nil
}

// profileMutationsClause selects a mutation profile's rows (a study can hold
// both its MUTATION_EXTENDED and MUTATION_UNCALLED profiles).
func (s Server) profileMutationsClause(a *core.SQLArgs, profileID string) string {
	return fmt.Sprintf("study_id = %s AND molecular_profile_id = %s", a.Str(s.profileStudy(profileID)), a.Str(profileID))
}

// sampleListClause restricts col to the members of a sample list that are
// samples of the profile's study (no-op for an empty list id).
func (s Server) sampleListClause(a *core.SQLArgs, col, listID, profileID string) string {
	if listID == "" {
		return ""
	}
	return fmt.Sprintf(" AND %s IN %s", col, s.BySampleListIn(listID, s.profileStudy(profileID)).ListMembers(a))
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

func (s Server) mutationsFetchProfile(w http.ResponseWriter, r *http.Request) {
	var f mutationFilter
	core.ReadBody(r, &f)
	if core.NoSamples(f.SampleIDs, f.SampleListID) {
		core.WriteJSON(w, []Mutation{})
		return
	}
	args := core.NewArgs()
	where := s.profileMutationsClause(args, r.PathValue("id"))
	// MutationFilter is EITHER explicit sample ids OR a sample list.
	if len(f.SampleIDs) > 0 {
		where += " AND sample_id IN " + args.Strs(f.SampleIDs)
	} else {
		where += s.sampleListClause(args, "sample_id", f.SampleListID, r.PathValue("id"))
	}
	where += geneFilter(f.EntrezGeneIDs)
	s.mutationsWhere(w, r, where, args, f.EntrezGeneIDs, 0)
}

func (s Server) mutationsFetch(w http.ResponseWriter, r *http.Request) {
	var f mutationFilter
	core.ReadBody(r, &f)
	// MutationMultipleStudyFilter is EITHER a set of (profile, sample) pairs OR a
	// list of molecular profiles (all their samples), each with an optional gene
	// filter. The results view uses the molecularProfileIds form.
	args := core.NewArgs()
	if len(f.SampleMolecularIdentifiers) > 0 {
		profiles := make([]string, len(f.SampleMolecularIdentifiers))
		for i, id := range f.SampleMolecularIdentifiers {
			profiles[i] = id.MolecularProfileID
		}
		study := s.profileStudies(profiles)
		triples := make([][3]string, len(f.SampleMolecularIdentifiers))
		for i, id := range f.SampleMolecularIdentifiers {
			triples[i] = [3]string{study[id.MolecularProfileID], id.MolecularProfileID, id.SampleID}
		}
		where := fmt.Sprintf("(study_id, molecular_profile_id, sample_id) IN %s%s", args.Triples(triples), geneFilter(f.EntrezGeneIDs))
		s.mutationsWhere(w, r, where, args, f.EntrezGeneIDs, 0)
		return
	}
	if len(f.MolecularProfileIDs) > 0 {
		study := s.profileStudies(f.MolecularProfileIDs)
		studies := make([]string, len(f.MolecularProfileIDs))
		for i, id := range f.MolecularProfileIDs {
			studies[i] = study[id]
		}
		where := fmt.Sprintf("study_id IN %s AND molecular_profile_id IN %s%s",
			args.Strs(studies), args.Strs(f.MolecularProfileIDs), geneFilter(f.EntrezGeneIDs))
		s.mutationsWhere(w, r, where, args, f.EntrezGeneIDs, 0)
		return
	}
	core.WriteJSON(w, []Mutation{})
}

// ---------- Gene panels ----------

type panelGeneRow struct {
	GenePanelID    string `json:"genePanelId"`
	Description    string `json:"description"`
	HugoGeneSymbol string `json:"hugoGeneSymbol"`
	EntrezGeneID   *int64 `json:"entrezGeneId"`
}

type PanelGene struct {
	EntrezGeneID   *int64 `json:"entrezGeneId,omitempty"`
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
	out := []GenePanel{}
	for _, r := range rows {
		if len(out) == 0 || out[len(out)-1].GenePanelID != r.GenePanelID {
			out = append(out, GenePanel{GenePanelID: r.GenePanelID, Description: r.Description})
		}
		p := &out[len(out)-1]
		p.Genes = append(p.Genes, PanelGene{EntrezGeneID: r.EntrezGeneID, HugoGeneSymbol: r.HugoGeneSymbol})
	}
	return out
}

func (s Server) genePanels(w http.ResponseWriter, r *http.Request) {
	rows, err := ch.Query[panelGeneRow](s.CH, panelSelect+" ORDER BY genePanelId, hugoGeneSymbol")
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	core.WriteJSON(w, groupPanels(rows))
}

// genePanelsFetch returns gene panels (with their gene lists) for the posted
// panel IDs (raw JSON array of strings) — used by results/patient views to build
// per-gene coverage.
func (s Server) genePanelsFetch(w http.ResponseWriter, r *http.Request) {
	var ids []string
	core.ReadBody(r, &ids)
	if len(ids) == 0 {
		core.WriteJSON(w, []GenePanel{})
		return
	}
	rows, err := ch.QueryP[panelGeneRow](s.CH, panelSelect+" WHERE genePanelId IN {ids:Array(String)} ORDER BY genePanelId, hugoGeneSymbol",
		ch.Params{"ids": ids})
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	core.WriteJSON(w, groupPanels(rows))
}

func (s Server) genePanel(w http.ResponseWriter, r *http.Request) {
	rows, err := ch.QueryP[panelGeneRow](s.CH, panelSelect+" WHERE genePanelId = {id:String} ORDER BY hugoGeneSymbol",
		ch.Params{"id": r.PathValue("id")})
	if err != nil {
		core.Fail(w, r, err)
		return
	}
	panels := groupPanels(rows)
	if len(panels) == 0 {
		http.Error(w, "gene panel not found", http.StatusNotFound)
		return
	}
	core.WriteJSON(w, panels[0])
}
