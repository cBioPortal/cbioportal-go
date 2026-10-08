package api

import (
	"fmt"
	"math"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"strings"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/internal/stats"
)

// Generic assay categorical and binary enrichments, as cbioportal's
// ExpressionEnrichmentServiceImpl + ExpressionEnrichmentUtil compute them:
//
//   - all groups must reference one molecular profile, of type GENERIC_ASSAY
//     and datatype CATEGORICAL / BINARY;
//   - for a patient_level profile each group keeps one case per patient;
//   - each case maps to its samples (enrichmentType=PATIENT: every sample of
//     the patient) that the profile covers; a group's values are those
//     samples' cells in case order, a missing cell being the empty string;
//   - groups are visited in Java HashMap order of their names.
//
// ClickHouse returns one row per (entity, group) holding the group's values
// in case order; the statistics run in Go. The order matters for Java's
// results: categories enter HashMaps in first-occurrence order (which fixes
// the contingency-table column order) and binary means and variances are
// floating-point sums over the values in that order.

// CategoricalGroupStatistics is the groupsStatistics element of a categorical
// enrichment: Java only sets the group name.
type CategoricalGroupStatistics struct {
	Name string `json:"name"`
}

// gaGroupValues holds, per entity, each group's values in case order
// (indexed like gaEnrichmentInput.groups; nil when the entity has no stored
// value in the group).
type gaGroupValues map[string][][]string

// gaEnrichmentInput is the resolved request: the groups in Java HashMap order
// with their profiled-sample counts, the profile's entities in storage order
// and the per-entity group values.
type gaEnrichmentInput struct {
	profileID string
	meta      profileMeta
	groups    []string
	sizes     []int
	entities  []string
	values    gaGroupValues
}

// groupValues returns the values of group gi for entity e: the stored
// sequence, or all-empty when nothing is stored.
func (in *gaEnrichmentInput) groupValues(e string, gi int) []string {
	if seqs := in.values[e]; seqs != nil && seqs[gi] != nil {
		return seqs[gi]
	}
	return make([]string, in.sizes[gi])
}

// gaEnrichmentParse validates the request the way the Java controller and
// service do and resolves it into per-group value counts. It writes the error
// response and returns nil when the request fails.
func (s *State) gaEnrichmentParse(w http.ResponseWriter, r *http.Request, datatype string) *gaEnrichmentInput {
	var groups []casesGroup
	readBody(r, &groups)
	patient := false
	switch et := strings.TrimSpace(r.URL.Query().Get("enrichmentType")); et {
	case "", "SAMPLE":
	case "PATIENT":
		patient = true
	default:
		writeError(w, http.StatusBadRequest, msgEnumMismatch("org.cbioportal.legacy.model.EnrichmentType", et))
		return nil
	}

	profileIDs := map[string]bool{}
	profileID := ""
	for _, g := range groups {
		for _, c := range g.MolecularProfileCaseIdentifiers {
			profileIDs[c.MolecularProfileID] = true
			profileID = c.MolecularProfileID
		}
	}
	switch {
	case len(profileIDs) > 1:
		writeError(w, http.StatusNotImplemented, "Requested API is not implemented yet")
		return nil
	case len(profileIDs) == 0:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("{}"))
		return nil
	}
	meta, ok := s.profileMeta(profileID)
	if !ok || meta.AlterationType != "GENERIC_ASSAY" || meta.Datatype != datatype {
		writeError(w, http.StatusNotFound, msgMolecularProfileNotFound(profileID))
		return nil
	}
	in := &gaEnrichmentInput{profileID: profileID, meta: meta}

	// Groups in the iteration order of the Java HashMap keyed on group name.
	names := make([]string, len(groups))
	byName := map[string]casesGroup{}
	for i, g := range groups {
		names[i] = g.Name
		byName[g.Name] = g
	}
	in.groups = javaHashMapOrder(names)

	cases := make([][]string, len(in.groups))
	for i, n := range in.groups {
		for _, c := range byName[n].MolecularProfileCaseIdentifiers {
			cases[i] = append(cases[i], c.CaseID)
		}
	}
	patientLevel := s.gaProfilePatientLevel(meta)
	if patientLevel {
		cases = s.gaOneCasePerPatient(meta.StudyID, cases)
	}
	keys, err := s.gaCaseMatrixKeys(meta.StudyID, profileID, cases, patient, patientLevel)
	if err != nil {
		fail(w, r, err)
		return nil
	}
	in.sizes = make([]int, len(keys))
	for i, k := range keys {
		in.sizes[i] = len(k)
	}
	in.entities, in.values, err = s.gaValueSequences(meta, keys)
	if err != nil {
		fail(w, r, err)
		return nil
	}
	return in
}

// gaProfilePatientLevel reports whether the profile's meta file declares
// patient_level: true (its matrix columns are then patient ids).
func (s *State) gaProfilePatientLevel(meta profileMeta) bool {
	type row struct {
		P string `json:"p"`
	}
	rows, err := cachedQueryP[row](s,
		"SELECT toString(patient_level) AS p FROM cbioportal_raw.genetic_profile "+
			"WHERE study_id = {study:String} AND profile_stable_id = {profile:String} LIMIT 1",
		ch.Params{"study": meta.StudyID, "profile": meta.StableID})
	return err == nil && len(rows) > 0 && strings.EqualFold(strings.TrimSpace(rows[0].P), "true")
}

// gaOneCasePerPatient keeps, per group, the first case of every patient, as
// filterMolecularProfileCaseSets does for patient-level profiles. Case ids
// that are no sample of the study share one (null) patient.
func (s *State) gaOneCasePerPatient(studyID string, cases [][]string) [][]string {
	var all []string
	for _, c := range cases {
		all = append(all, c...)
	}
	patientOf := map[string]string{}
	if len(all) > 0 {
		p := ch.Params{}
		rows, _ := ch.QueryP[idRow](s.CH,
			"SELECT sample_id AS sampleId, patient_id AS patientId FROM "+bySamples(studyID, all).from(paramsBinder(p), tSample), p)
		for _, r := range rows {
			patientOf[r.SampleID] = "p:" + r.PatientID
		}
	}
	out := make([][]string, len(cases))
	for i, c := range cases {
		seen := map[string]bool{}
		for _, id := range c {
			p := patientOf[id]
			if seen[p] {
				continue
			}
			seen[p] = true
			out[i] = append(out[i], id)
		}
	}
	return out
}

// gaCaseMatrixKeys maps each group's cases to the profiled samples they
// stand for and returns, per group and in case order, the matrix column
// (sample id, or patient id for a patient-level profile) of each such sample.
func (s *State) gaCaseMatrixKeys(studyID, profileID string, cases [][]string, patient, patientLevel bool) ([][]string, error) {
	var all []string
	for _, c := range cases {
		all = append(all, c...)
	}
	idCol := "sample_id"
	if patient {
		idCol = "patient_id"
	}
	samplesOf := map[string][]idRow{}
	if len(all) > 0 {
		p := ch.Params{"study": studyID, "ids": all, "profile": profileID}
		rows, err := ch.QueryP[idRow](s.CH, fmt.Sprintf(
			"SELECT sample_id AS sampleId, patient_id AS patientId FROM %s "+
				"AND %s IN {ids:Array(String)} "+
				"AND sample_id IN (SELECT sample_id FROM sample_profile WHERE study_id = {study:String} AND molecular_profile_id = {profile:String}) "+
				"ORDER BY sample_id", byStudy(studyID).from(paramsBinder(p), tSample), idCol), p)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			k := r.SampleID
			if patient {
				k = r.PatientID
			}
			samplesOf[k] = append(samplesOf[k], r)
		}
	}
	out := make([][]string, len(cases))
	for i, c := range cases {
		for _, id := range c {
			for _, smp := range samplesOf[id] {
				key := smp.SampleID
				if patientLevel {
					key = smp.PatientID
				}
				out[i] = append(out[i], key)
			}
		}
	}
	return out, nil
}

// gaValueSequences returns the profile's entities in storage order and, per
// entity and group, the (Java-truncated) values at the group's matrix keys,
// with the empty string for a cell without a stored value.
func (s *State) gaValueSequences(meta profileMeta, keys [][]string) ([]string, gaGroupValues, error) {
	args := newArgs()
	from := byStudy(meta.StudyID).from(args, rawMatrix("generic_assay_matrix")) + " AND profile_stable_id = " + args.str(meta.StableID)
	type entityRow struct {
		E string `json:"e"`
	}
	ents, err := queryA[entityRow](s.CH, fmt.Sprintf(
		"SELECT entity_stable_id AS e FROM %s "+
			"GROUP BY entity_stable_id ORDER BY min(%s), entity_stable_id", from, s.rawFileOrder("generic_assay_matrix", "")), args)
	if err != nil {
		return nil, nil, err
	}
	entities := make([]string, len(ents))
	for i, e := range ents {
		entities[i] = e.E
	}

	var parts []string
	for gi, k := range keys {
		if len(k) == 0 {
			continue
		}
		list := args.strs(k)
		parts = append(parts, fmt.Sprintf(
			"SELECT e, toUInt32(%d) AS grp, arrayMap(k -> m[k], %s) AS vs FROM ("+
				"SELECT entity_stable_id AS e, mapFromArrays(groupArray(sample_id), groupArray(%s)) AS m "+
				"FROM %s AND sample_id IN %s GROUP BY e)",
			gi, list, javaTruncatedValueSQL("trimBoth(value)"), from, list))
	}
	values := gaGroupValues{}
	if len(parts) == 0 {
		return entities, values, nil
	}
	type seqRow struct {
		E   string   `json:"e"`
		Grp uint32   `json:"grp"`
		Vs  []string `json:"vs"`
	}
	rows, err := queryA[seqRow](s.CH, strings.Join(parts, " UNION ALL "), args)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range rows {
		v := values[r.E]
		if v == nil {
			v = make([][]string, len(keys))
			values[r.E] = v
		}
		v[r.Grp] = r.Vs
	}
	return entities, values, nil
}

// gaMetaProperties returns the SUMMARY meta properties of the profile's
// entities.
func (s *State) gaMetaProperties(meta profileMeta) map[string]javaStringMap {
	args := newArgs()
	props, _ := s.genericAssayMetaProperties(fmt.Sprintf(
		"study_id = %s AND profile_stable_id = %s", args.str(meta.StudyID), args.str(meta.StableID)), args)
	return summaryMetaProperties(props)
}

func metaOrEmpty(props map[string]javaStringMap, stableID string) javaStringMap {
	return props[stableID]
}

// genericAssayCategoricalEnrichmentsFetch is
// /generic-assay-categorical-enrichments/fetch: per entity, a chi-square test
// of independence between group and category.
func (s *State) genericAssayCategoricalEnrichmentsFetch(w http.ResponseWriter, r *http.Request) {
	in := s.gaEnrichmentParse(w, r, "CATEGORICAL")
	if in == nil {
		return
	}
	type result struct {
		stable string
		stats  []CategoricalGroupStatistics
		p      float64
	}

	// Groups with fewer than two samples take no part; which groups those
	// are does not depend on the entity.
	var rowGroups []int
	var rowNames []string
	for gi, n := range in.sizes {
		if n >= 2 {
			rowGroups = append(rowGroups, gi)
			rowNames = append(rowNames, in.groups[gi])
		}
	}
	out := []GenericAssayCategoricalEnrichment{}
	if len(rowGroups) < 2 {
		writeJSON(w, out)
		return
	}
	// Contingency-table rows follow the HashMap holding only these groups.
	tableOrder := map[string]int{}
	for i, n := range javaHashMapOrder(rowNames) {
		tableOrder[n] = i
	}
	tableRows := append([]int(nil), rowGroups...)
	sort.SliceStable(tableRows, func(i, j int) bool {
		return tableOrder[in.groups[tableRows[i]]] < tableOrder[in.groups[tableRows[j]]]
	})
	gs := make([]CategoricalGroupStatistics, len(rowGroups))
	for i, gi := range rowGroups {
		gs[i] = CategoricalGroupStatistics{Name: in.groups[gi]}
	}

	results := make([]result, 0, len(in.entities))
	for _, e := range in.entities {
		// Each group's category counts live in a HashMap filled in value
		// order; the column set is a HashSet filled row by row.
		rowCounts := make([]map[string]int, len(tableRows))
		var cats []string
		for i, gi := range tableRows {
			m := map[string]int{}
			var firstSeen []string
			for _, v := range in.groupValues(e, gi) {
				if _, ok := m[v]; !ok {
					firstSeen = append(firstSeen, v)
				}
				m[v]++
			}
			rowCounts[i] = m
			cats = append(cats, javaHashMapOrder(firstSeen)...)
		}
		cats = javaHashMapOrder(cats)
		p := 1.0
		if len(cats) > 1 {
			table := make([][]int64, len(tableRows))
			for i := range tableRows {
				table[i] = make([]int64, len(cats))
				for j, c := range cats {
					table[i][j] = int64(rowCounts[i][c])
				}
			}
			p = stats.CommonsChiSquareTest(table)
		}
		if math.IsNaN(p) {
			p = 1
		}
		results = append(results, result{stable: e, stats: gs, p: p})
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].p < results[j].p })
	ps := make([]float64, len(results))
	for i, res := range results {
		ps[i] = res.p
	}
	qs := javaQValues(ps)
	props := s.gaMetaProperties(in.meta)
	for i, res := range results {
		out = append(out, GenericAssayCategoricalEnrichment{
			GenericEntityMetaProperties: metaOrEmpty(props, res.stable),
			GroupsStatistics:            res.stats,
			PValue:                      res.p,
			QValue:                      qs[i],
			StableID:                    res.stable,
		})
	}
	writeJSON(w, out)
}

// genericAssayBinaryEnrichmentsFetch is /generic-assay-binary-enrichments/fetch:
// values true/yes count as 1 and false/no as 0; per entity, groups with at
// least two non-empty values are compared with Welch's t-test (two groups) or
// a one-way ANOVA.
func (s *State) genericAssayBinaryEnrichmentsFetch(w http.ResponseWriter, r *http.Request) {
	in := s.gaEnrichmentParse(w, r, "BINARY")
	if in == nil {
		return
	}
	rnaSeq := strings.Contains(in.profileID, "rna_seq")
	type result struct {
		stable string
		counts []GenericAssayCountSummary
		stats  []GroupStatistics
		p      float64
	}
	results := make([]result, 0, len(in.entities))
	for _, e := range in.entities {
		var res result
		var grouped [][]float64
		for gi, name := range in.groups {
			var vals []string
			for _, v := range in.groupValues(e, gi) {
				if v != "" {
					vals = append(vals, v)
				}
			}
			if len(vals) < 2 {
				continue
			}
			values := make([]float64, len(vals))
			for i, v := range vals {
				x, ok := javaParseBinaryValue(v)
				if !ok {
					writeError(w, http.StatusInternalServerError, `For input string: "`+v+`"`)
					return
				}
				if rnaSeq {
					x = math.Log1p(math.Max(x, 0)) / math.Ln2
				}
				values[i] = x
			}
			ones := 0
			for _, x := range values {
				if x == 1 {
					ones++
				}
			}
			mean := stats.CommonsMean(values)
			sd := stats.CommonsStandardDeviation(values)
			if math.IsNaN(mean) || math.IsNaN(sd) {
				continue
			}
			grouped = append(grouped, values)
			res.stats = append(res.stats, GroupStatistics{MeanExpression: mean, Name: name, StandardDeviation: sd})
			res.counts = append(res.counts, GenericAssayCountSummary{Count: ones, Name: name, TotalCount: len(values)})
		}
		if len(res.stats) < 2 {
			continue
		}
		if len(grouped) == 2 {
			res.p = stats.CommonsTTest(grouped[0], grouped[1])
		} else {
			res.p = stats.CommonsAnovaPValue(grouped)
		}
		if math.IsNaN(res.p) {
			continue
		}
		res.stable = e
		results = append(results, res)
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].p < results[j].p })
	ps := make([]float64, len(results))
	for i, res := range results {
		ps[i] = res.p
	}
	qs := javaQValues(ps)
	props := s.gaMetaProperties(in.meta)
	out := make([]GenericAssayBinaryEnrichment, len(results))
	for i, res := range results {
		out[i] = GenericAssayBinaryEnrichment{
			Counts:                      res.counts,
			GenericEntityMetaProperties: metaOrEmpty(props, res.stable),
			GroupsStatistics:            res.stats,
			PValue:                      res.p,
			QValue:                      qs[i],
			StableID:                    res.stable,
		}
	}
	writeJSON(w, out)
}

// javaParseBinaryValue maps a binary generic assay value to a number as
// ExpressionEnrichmentUtil does: "true"/"yes" are 1, "false"/"no" 0, and
// anything else goes through Double.parseDouble (ok is false where that
// throws).
func javaParseBinaryValue(v string) (float64, bool) {
	switch v {
	case "true", "yes":
		return 1, true
	case "false", "no":
		return 0, true
	}
	s := strings.TrimFunc(v, func(r rune) bool { return r <= ' ' })
	unsigned := strings.TrimLeft(s, "+-")
	switch unsigned {
	case "NaN", "Infinity":
		if len(s)-len(unsigned) > 1 || (unsigned == "NaN" && s != "NaN") {
			return 0, false
		}
		if unsigned == "NaN" {
			return math.NaN(), true
		}
		if strings.HasPrefix(s, "-") {
			return math.Inf(-1), true
		}
		return math.Inf(1), true
	}
	lower := strings.ToLower(unsigned)
	if strings.Contains(lower, "inf") || strings.Contains(lower, "nan") || strings.Contains(s, "_") {
		return 0, false
	}
	if n := len(s); n > 0 && strings.ContainsRune("fFdD", rune(s[n-1])) && !strings.HasPrefix(lower, "0x") {
		s = s[:n-1]
	}
	x, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return x, true
}

// javaQValues is FisherExactTestCalculator.calcqValue over p-values sorted
// ascending, in BigDecimal arithmetic: each p is BigDecimal.valueOf(p) (the
// decimal digits of Double.toString), and walking from the largest p the
// q-value is min(previous q, p * n / rank) with the quotient rounded half-up
// to p's own scale.
func javaQValues(sortedP []float64) []float64 {
	n := len(sortedP)
	q := make([]float64, n)
	var cached *big.Rat
	for i := 0; i < n; i++ {
		idx := n - 1 - i
		unscaled, scale := javaBigDecimalOf(sortedP[idx])
		var v *big.Rat
		if i == 0 {
			v = decimalRat(unscaled, scale)
		} else {
			num := new(big.Int).Mul(unscaled, big.NewInt(int64(n)))
			v = decimalRat(divHalfUp(num, big.NewInt(int64(n-i))), scale)
			if cached.Cmp(v) <= 0 {
				v = cached
			}
		}
		cached = v
		f, _ := v.Float64()
		q[idx] = f
	}
	return q
}

// javaBigDecimalOf returns the unscaled value and scale of
// BigDecimal.valueOf(d), i.e. of Double.toString(d): the shortest decimal
// digits with at least one fraction digit.
func javaBigDecimalOf(d float64) (*big.Int, int) {
	if d == 0 || math.IsNaN(d) || math.IsInf(d, 0) {
		return big.NewInt(0), 1
	}
	s := strconv.FormatFloat(d, 'e', -1, 64)
	mant, expStr, _ := strings.Cut(s, "e")
	exp, _ := strconv.Atoi(expStr)
	neg := strings.HasPrefix(mant, "-")
	digits := strings.ReplaceAll(strings.TrimPrefix(mant, "-"), ".", "")
	frac := len(digits) - 1
	var scale int
	if a := math.Abs(d); a >= 1e-3 && a < 1e7 {
		scale = frac - exp
		if scale < 1 {
			digits += strings.Repeat("0", 1-scale)
			scale = 1
		}
	} else {
		if frac < 1 {
			digits += "0"
			frac = 1
		}
		scale = frac - exp
	}
	u, _ := new(big.Int).SetString(digits, 10)
	if neg {
		u.Neg(u)
	}
	return u, scale
}

// divHalfUp divides a by b (b > 0) rounding half away from zero.
func divHalfUp(a, b *big.Int) *big.Int {
	q, r := new(big.Int).QuoRem(a, b, new(big.Int))
	r2 := new(big.Int).Abs(r)
	r2.Lsh(r2, 1)
	if r2.Cmp(b) >= 0 {
		if a.Sign() < 0 {
			q.Sub(q, big.NewInt(1))
		} else {
			q.Add(q, big.NewInt(1))
		}
	}
	return q
}

// decimalRat is unscaled * 10^-scale.
func decimalRat(unscaled *big.Int, scale int) *big.Rat {
	if scale >= 0 {
		return new(big.Rat).SetFrac(unscaled, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil))
	}
	return new(big.Rat).SetInt(new(big.Int).Mul(unscaled, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-scale)), nil)))
}
