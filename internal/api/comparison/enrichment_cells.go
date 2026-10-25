package comparison

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// The per-group statistics of a continuous enrichment, as rows (symbol,
// entrez, grp, n, mean, var, pos): the count, mean and sample variance of a
// gene's or entity's numeric values within each group, pos its first
// storage position. A cell counts for every group listing its sample.

// groupMemberSQL is the expression listing the groups (of cg, those read
// from table) whose triples hold a row's (study_id, profile_stable_id,
// sample_id), as an array; values are bound in args.
func groupMemberSQL(args *core.SQLArgs, cg []contGroup, table string) string {
	var member []string
	for gi, g := range cg {
		if g.table == table {
			member = append(member, fmt.Sprintf("if((study_id, profile_stable_id, sample_id) IN %s, %d, -1)", args.Triples(g.triples), gi))
		}
	}
	return "arrayFilter(g -> g >= 0, [" + strings.Join(member, ", ") + "])"
}

// matrixStats aggregates the groups' cells of the staging matrices. Each
// table is read once for all the groups it serves, held to their samples
// and, where the staging records each profile's file (source_file), to the
// requested profiles' files, which lead its sort key after the study.
func (s Server) matrixStats(args *core.SQLArgs, cg []contGroup, keyCol string, withGene bool) (string, error) {
	var tables []string
	for _, g := range cg {
		if g.table != "" && !slices.Contains(tables, g.table) {
			tables = append(tables, g.table)
		}
	}
	key := keyCol + " AS symbol, CAST(NULL AS Nullable(Int64)) AS entrez"
	if withGene {
		key = "hugo_symbol AS symbol, toInt64OrNull(entrez_gene_id) AS entrez"
	}
	var parts []string
	for _, table := range tables {
		var all [][3]string
		var profiles [][2]string
		seen := map[[3]string]bool{}
		for _, g := range cg {
			if g.table != table {
				continue
			}
			for _, tr := range g.triples {
				if !seen[tr] {
					seen[tr] = true
					all = append(all, tr)
				}
				if p := [2]string{tr[0], tr[1]}; !slices.Contains(profiles, p) {
					profiles = append(profiles, p)
				}
			}
		}
		where := "(study_id, profile_stable_id, sample_id) IN " + args.Triples(all)
		if slices.Contains(s.RawColumns(table), "source_file") && slices.Contains(s.RawColumns("profile_sample"), "source_file") {
			where += " AND (study_id, source_file) IN (SELECT study_id, source_file FROM cbioportal_raw.profile_sample " +
				"WHERE (study_id, profile_stable_id) IN " + args.Pairs(profiles) + ")"
		}
		grp := "toUInt32(arrayJoin(" + groupMemberSQL(args, cg, table) + ")) AS grp"
		x := "toFloat64OrNull(" + core.JavaTruncatedValueSQL("value") + ") AS x"
		keyMapped := false
		if withGene {
			var err error
			if keyMapped, err = s.GeneKeyMapped(table); err != nil {
				return "", err
			}
		}
		if keyMapped {
			// Values are those of the genes the importer stored each line
			// under (gene_key_map); lines it skipped are left out.
			ent := "''"
			if slices.Contains(s.RawColumns(table), "entrez_gene_id") {
				ent = "entrez_gene_id"
			}
			parts = append(parts, fmt.Sprintf(
				"SELECT gk.resolved_hugo_gene_symbol AS symbol, toNullable(gk.resolved_entrez_gene_id) AS entrez, t.grp AS grp, "+
					"if(position(concat(t.study_id, '_', t.profile_stable_id), 'rna_seq') > 0, log2(greatest(t.x, 0) + 1), t.x) AS v, t.pos_ AS pos_ "+
					"FROM (SELECT study_id, profile_stable_id, %s AS key_sym, %s AS key_ent, %s AS pos_, %s, %s FROM cbioportal_raw.%s "+
					"WHERE %s) t "+
					"INNER JOIN (SELECT * FROM gene_key_map WHERE source = '%s' AND (study_id, profile_stable_id) IN %s) gk ON gk.study_id = t.study_id "+
					"AND gk.profile_stable_id = t.profile_stable_id AND gk.hugo_symbol = t.key_sym AND gk.entrez_gene_id = t.key_ent "+
					"WHERE isFinite(t.x)",
				s.GeneKeySymbol(table, ""), ent, s.RawFileOrder(table, ""), x, grp, table, where, table, args.Pairs(profiles)))
			continue
		}
		parts = append(parts, fmt.Sprintf(
			"SELECT %s, grp, "+
				"if(position(concat(study_id, '_', profile_stable_id), 'rna_seq') > 0, log2(greatest(x, 0) + 1), x) AS v, pos_ "+
				"FROM (SELECT *, %s AS pos_, %s, %s FROM cbioportal_raw.%s WHERE %s) WHERE isFinite(x)",
			key, s.RawFileOrder(table, ""), x, grp, table, where))
	}
	return "SELECT symbol, entrez, grp, count() AS n, avg(v) AS mean, varSampStable(v) AS var, sum(v) AS sum, sum(v * v) AS sumsq, min(pos_) AS pos FROM (" +
		strings.Join(parts, " UNION ALL ") + ") GROUP BY symbol, entrez, grp", nil
}

// profileSampleRow is a sample of a profile with its position in the
// profile's values.
type profileSampleRow struct {
	SampleID  string `json:"sampleId"`
	PatientID string `json:"patientId"`
	Position  uint64 `json:"position"`
}

// profileRowStats is matrixStats over production's genetic_alteration,
// where the reference database holds it (a database built from production):
// one row per gene or entity of a profile, sorted by profile, with the
// values of the profile's samples in genetic_profile_samples order. The read
// is held to the profile's rows by the table key, where the staging matrices
// over production's derived tables read every cell of the study (genes) or
// of every study with the profile type (generic assay), and each row's
// groups are aggregated from its own values. A group's values are aggregated
// in the order the derived tables store them, the order matrixStats reads
// them in there: by sample, for generic assay by patient, then sample. false
// when the database holds no genetic_alteration or the groups do not read
// one profile.
func (s Server) profileRowStats(args *core.SQLArgs, groups []casesGroup, cg []contGroup, withGene bool) (string, bool, error) {
	if !slices.Contains(s.RefColumns("genetic_alteration"), "values") || !slices.Contains(s.RefColumns("genetic_profile"), "stable_id") ||
		!slices.Contains(s.RawColumns("profile_sample"), "position") {
		return "", false, nil
	}
	mpid, study, profile := "", "", ""
	for gi, g := range groups {
		if cg[gi].table == "" {
			continue
		}
		for _, c := range g.MolecularProfileCaseIdentifiers {
			m, ok := s.ProfileMeta(c.MolecularProfileID)
			if !ok {
				continue
			}
			if mpid == "" {
				mpid, study, profile = c.MolecularProfileID, m.StudyID, m.StableID
			} else if c.MolecularProfileID != mpid {
				return "", false, nil
			}
		}
	}
	type idRow struct {
		ID int64 `json:"id"`
	}
	ids, err := core.CachedQueryP[idRow](s.State,
		"SELECT genetic_profile_id AS id FROM cbioportal_ref.genetic_profile WHERE stable_id = {id:String}", ch.Params{"id": mpid})
	if err != nil {
		return "", false, err
	}
	if len(ids) != 1 {
		return "", false, nil
	}

	patient := "'' AS patientId"
	join := ""
	if !withGene {
		patient = "ifNull(s.patient_id, '') AS patientId"
		join = " LEFT JOIN (SELECT sample_id, patient_id FROM " + core.ByStudy(study).From(args, core.TSample) + ") s ON s.sample_id = ps.sample_id"
	}
	q := "SELECT ps.sample_id AS sampleId, " + patient + ", toUInt64(ps.position) AS position FROM cbioportal_raw.profile_sample ps" + join +
		" WHERE ps.study_id = " + args.Str(study) + " AND ps.profile_stable_id = " + args.Str(profile) + " AND ps.kind = 'header'"
	samples, err := core.QueryA[profileSampleRow](s.CH, q, args)
	if err != nil {
		return "", false, err
	}
	width := uint64(0)
	bySample := map[string]profileSampleRow{}
	for _, r := range samples {
		if r.Position == 0 {
			return "", false, nil
		}
		width = max(width, r.Position)
		if _, dup := bySample[r.SampleID]; !dup {
			bySample[r.SampleID] = r
		}
	}

	value := rnaSeqValueSQL("x", strings.Contains(study+"_"+profile, "rna_seq"))
	var vals, stats []string
	for gi, g := range cg {
		var members []profileSampleRow
		seen := map[string]bool{}
		for _, tr := range g.triples {
			if r, ok := bySample[tr[2]]; ok && !seen[tr[2]] {
				seen[tr[2]] = true
				members = append(members, r)
			}
		}
		if len(members) == 0 {
			continue
		}
		slices.SortFunc(members, func(a, b profileSampleRow) int {
			return cmp.Or(cmp.Compare(a.PatientID, b.PatientID), cmp.Compare(a.SampleID, b.SampleID))
		})
		rank := map[uint64]int{}
		for i, r := range members {
			rank[r.Position] = i + 1
		}
		mask := make([]string, width)
		var keys []string
		for p := uint64(1); p <= width; p++ {
			if k, ok := rank[p]; ok {
				mask[p-1] = "1"
				keys = append(keys, strconv.Itoa(k))
			} else {
				mask[p-1] = "0"
			}
		}
		// The group's values in aggregation order, parsed as the matrices'
		// cells are; non-numeric ones are left out.
		vals = append(vals, fmt.Sprintf(
			"arrayMap(x -> %s, arrayMap(x -> assumeNotNull(x), arrayFilter(x -> isNotNull(x) AND isFinite(assumeNotNull(x)), "+
				"arrayMap(x -> toFloat64OrNull(%s), arraySort((x, k) -> k, arrayFilter((x, m) -> m = 1, s, [%s]), [%s]))))) AS v%d",
			value, core.JavaTruncatedValueSQL("x"), strings.Join(mask, ","), strings.Join(keys, ","), gi))
		stats = append(stats, fmt.Sprintf("(toUInt32(%[1]d), length(v%[1]d), arrayReduce('avg', v%[1]d), arrayReduce('varSampStable', v%[1]d), "+
			"arrayReduce('sum', v%[1]d), arrayReduce('sum', arrayMap(x -> x * x, v%[1]d)))", gi))
	}
	if len(stats) == 0 {
		return "", false, nil
	}

	pid := strconv.FormatInt(ids[0].ID, 10)
	key := "g.hugo_gene_symbol AS symbol, toNullable(g.entrez_gene_id) AS entrez"
	entities := "INNER JOIN (SELECT genetic_entity_id, hugo_gene_symbol, entrez_gene_id FROM cbioportal_ref.gene) g " +
		"ON g.genetic_entity_id = t.genetic_entity_id"
	if !withGene {
		key = "ifNull(e.stable_id, '') AS symbol, CAST(NULL AS Nullable(Int64)) AS entrez"
		entities = "INNER JOIN (SELECT id, stable_id FROM cbioportal_ref.genetic_entity WHERE id IN " +
			"(SELECT genetic_entity_id FROM cbioportal_ref.genetic_alteration WHERE genetic_profile_id = " + pid + ")) e " +
			"ON e.id = t.genetic_entity_id"
	}
	return fmt.Sprintf(
		"SELECT %s, t.grp AS grp, t.n AS n, t.mean AS mean, t.var AS var, t.sum AS sum, t.sumsq AS sumsq, (toUInt32(0), toUInt64(0), toUInt64(0), toUInt64(0)) AS pos "+
			"FROM (SELECT genetic_entity_id, tupleElement(st, 1) AS grp, tupleElement(st, 2) AS n, tupleElement(st, 3) AS mean, "+
			"tupleElement(st, 4) AS var, tupleElement(st, 5) AS sum, tupleElement(st, 6) AS sumsq FROM (SELECT genetic_entity_id, %s "+
			"FROM (SELECT genetic_entity_id, arrayResize(splitByChar(',', substring(values, 1, -1)), %d) AS s "+
			"FROM cbioportal_ref.genetic_alteration FINAL WHERE genetic_profile_id = %s)) "+
			"ARRAY JOIN [%s] AS st) t %s WHERE t.n > 0",
		key, strings.Join(vals, ", "), width, pid, strings.Join(stats, ", "), entities), true, nil
}
