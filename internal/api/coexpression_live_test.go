package api

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// TestLiveCoExpressionByProfileRow checks that co-expression over
// production's genetic_alteration (one row per gene of a profile, as a
// database built from production holds it) answers as the read of
// genetic_alteration_derived does. The fixture database's values are
// copied into that layout in scratch staging and reference databases.
func TestLiveCoExpressionByProfileRow(t *testing.T) {
	derived, admin := liveFixtureState(t, fmt.Sprintf("coexp-derived-%d", os.Getpid()))
	db, raw, ref := derived.CH.Database(), derived.CH.Database()+"_raw", "cbioportal_ref"
	scratch := fmt.Sprintf("coexp_test_%d", os.Getpid())
	sRaw, sRef := scratch+"_raw", scratch+"_ref"
	exec := func(sql string) {
		t.Helper()
		if _, err := admin.Execute(sql); err != nil {
			t.Fatalf("%v\n%s", err, sql)
		}
	}
	for _, d := range []string{sRaw, sRef} {
		exec("DROP DATABASE IF EXISTS `" + d + "`")
		exec("CREATE DATABASE `" + d + "`")
		t.Cleanup(func() { admin.Execute("DROP DATABASE IF EXISTS `" + d + "`") })
	}
	// Views over the fixture's staging and reference tables, with the
	// profile samples' positions that production's genetic_profile_samples
	// gives them.
	type nameRow struct {
		Database string `json:"database"`
		Name     string `json:"name"`
	}
	tables, err := ch.Query[nameRow](admin, "SELECT database, name FROM system.tables WHERE database IN ('"+
		ch.SQLLit(raw)+"', '"+ch.SQLLit(ref)+"') AND name NOT IN ('genetic_profile', 'genetic_alteration')")
	if err != nil {
		t.Fatal(err)
	}
	for _, tb := range tables {
		to, sel := sRef, "*"
		if tb.Database == raw {
			to = sRaw
			if tb.Name == "profile_sample" {
				sel = "*, row_num AS position"
			}
		}
		exec(fmt.Sprintf("CREATE VIEW `%s`.`%s` AS SELECT %s FROM `%s`.`%s`", to, tb.Name, sel, tb.Database, tb.Name))
	}
	exec(fmt.Sprintf("CREATE TABLE `%[1]s`.genetic_profile ENGINE = MergeTree ORDER BY genetic_profile_id AS "+
		"SELECT toInt64(row_number() OVER (ORDER BY study_id, profile_stable_id)) AS genetic_profile_id, "+
		"concat(study_id, '_', profile_stable_id) AS stable_id, study_id, profile_stable_id "+
		"FROM (SELECT DISTINCT study_id, profile_stable_id FROM `%[2]s`.profile_sample WHERE kind = 'header')", sRef, raw))
	// Each gene's values in the profile's sample order, with the trailing
	// comma production stores; '' where a sample has no value.
	exec(fmt.Sprintf("CREATE TABLE `%[1]s`.genetic_alteration ENGINE = ReplacingMergeTree ORDER BY (genetic_profile_id, genetic_entity_id) AS "+
		"SELECT p.genetic_profile_id AS genetic_profile_id, g.genetic_entity_id AS genetic_entity_id, "+
		"concat(arrayStringConcat(arrayMap(i -> m[i], range(1, toUInt64(w.width) + 1)), ','), ',') AS values "+
		"FROM (SELECT d.cancer_study_identifier AS study_id, d.profile_type AS profile, d.hugo_gene_symbol AS hugo, "+
		"mapFromArrays(groupArray(ps.row_num), groupArray(ifNull(d.alteration_value, ''))) AS m "+
		"FROM `%[2]s`.genetic_alteration_derived d INNER JOIN `%[3]s`.profile_sample ps ON ps.study_id = d.cancer_study_identifier "+
		"AND ps.profile_stable_id = d.profile_type AND concat(ps.study_id, '_', ps.sample_id) = d.sample_unique_id AND ps.kind = 'header' "+
		"GROUP BY study_id, profile, hugo) v "+
		"INNER JOIN (SELECT study_id, profile_stable_id, max(row_num) AS width FROM `%[3]s`.profile_sample WHERE kind = 'header' "+
		"GROUP BY study_id, profile_stable_id) w ON w.study_id = v.study_id AND w.profile_stable_id = v.profile "+
		"INNER JOIN `%[1]s`.genetic_profile p ON p.study_id = v.study_id AND p.profile_stable_id = v.profile "+
		"INNER JOIN `%[4]s`.gene g ON g.hugo_gene_symbol = v.hugo", sRef, db, raw, ref))

	comment := fmt.Sprintf("coexp-rows-%d", os.Getpid())
	t.Setenv("CLICKHOUSE_LOG_COMMENT", comment)
	c := ch.New(os.Getenv("CLICKHOUSE_TEST_URL"), db)
	c.SetDatabaseAliases(sRaw, sRef)
	byRow := NewState(c, Options{})

	type geneRow struct {
		Profile string `json:"profile"`
		Entrez  int64  `json:"entrez"`
	}
	genes, err := ch.Query[geneRow](admin, fmt.Sprintf(
		"SELECT DISTINCT concat(d.cancer_study_identifier, '_', d.profile_type) AS profile, g.entrez_gene_id AS entrez "+
			"FROM `%s`.genetic_alteration_derived d INNER JOIN `%s`.gene g ON g.hugo_gene_symbol = d.hugo_gene_symbol "+
			"WHERE d.cancer_study_identifier = 'study_es_0' AND d.profile_type IN ('mrna', 'mrna_median_Zscores', 'methylation_hm27') "+
			"ORDER BY profile, entrez", db, ref))
	if err != nil {
		t.Fatal(err)
	}
	if len(genes) < 10 {
		t.Fatalf("only %d fixture genes", len(genes))
	}
	type request struct{ a, b, body string }
	var requests []request
	for _, g := range genes {
		requests = append(requests,
			request{g.Profile, g.Profile, fmt.Sprintf(`{"entrezGeneId":%d,"sampleListId":"study_es_0_all"}`, g.Entrez)},
			request{g.Profile, g.Profile, fmt.Sprintf(`{"entrezGeneId":%d,"sampleIds":["TCGA-A1-A0SB-01","TCGA-A1-A0SD-01","TCGA-A1-A0SE-01","TCGA-A1-A0SF-01","TCGA-A1-A0SH-01","TCGA-A1-A0SI-01","TCGA-A1-A0SJ-01"]}`, g.Entrez)})
		if g.Profile == "study_es_0_mrna" {
			requests = append(requests,
				request{g.Profile, "study_es_0_mrna_median_Zscores", fmt.Sprintf(`{"entrezGeneId":%d,"sampleListId":"study_es_0_all"}`, g.Entrez)},
				request{g.Profile, "acc_tcga_rna_seq_v2_mrna", fmt.Sprintf(`{"entrezGeneId":%d,"sampleListId":"study_es_0_all"}`, g.Entrez)})
		}
	}
	hDerived, hRow := NewRouter(derived), NewRouter(byRow)
	nonEmpty := 0
	for _, rq := range requests {
		path := "/api/molecular-profiles/co-expressions/fetch?molecularProfileIdA=" + rq.a + "&molecularProfileIdB=" + rq.b + "&threshold=0"
		r := fixtureRequest{method: "POST", path: path, body: rq.body}
		want, got := serve(hDerived, r), serve(hRow, r)
		if want.Code != got.Code {
			t.Errorf("%s %s: status %d, want %d: %s", path, rq.body, got.Code, want.Code, got.Body)
			continue
		}
		var w, g []map[string]any
		json.Unmarshal(want.Body.Bytes(), &w)
		json.Unmarshal(got.Body.Bytes(), &g)
		if len(w) > 0 {
			nonEmpty++
		}
		if len(w) != len(g) {
			t.Errorf("%s %s: %d genes, want %d", path, rq.body, len(g), len(w))
			continue
		}
		for i := range w {
			for _, k := range []string{"geneticEntityId", "geneticEntityType", "spearmansCorrelation", "pValue"} {
				wv, gv := w[i][k], g[i][k]
				wf, wok := wv.(float64)
				gf, gok := gv.(float64)
				if wok && gok {
					if d := wf - gf; d > 1e-12 || d < -1e-12 {
						t.Errorf("%s %s: gene %v %s = %v, want %v", path, rq.body, w[i]["geneticEntityId"], k, gv, wv)
					}
				} else if wv != gv {
					t.Errorf("%s %s: gene %v %s = %v, want %v", path, rq.body, w[i]["geneticEntityId"], k, gv, wv)
				}
			}
		}
	}
	if nonEmpty < len(requests)/2 {
		t.Errorf("only %d of %d requests answered genes", nonEmpty, len(requests))
	}
	// The answers came from genetic_alteration.
	exec("SYSTEM FLUSH LOGS")
	out, err := admin.Execute("SELECT count() FROM system.query_log WHERE event_date >= yesterday() AND type = 'QueryFinish' " +
		"AND log_comment = '" + comment + "' AND query LIKE '%rankCorr%' AND query LIKE '%genetic_alteration FINAL%' AND query NOT LIKE '%genetic_alteration_derived%'")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) == "0" {
		t.Error("no co-expression read genetic_alteration")
	}
}
