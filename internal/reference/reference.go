// Package reference loads the cBioPortal gene reference (Entrez ↔ Hugo + aliases),
// the reference genomes with each gene's chromosome/cytoband, and the
// cancer-type hierarchy from the datahub seed MySQL dump into a ClickHouse
// reference database. The seed stores these tables as `INSERT INTO … VALUES
// (…),(…)` lines whose tuple syntax ClickHouse parses natively via FORMAT
// Values, so we stream the tuple bodies straight in.
package reference

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"os"
	"strings"

	ch "github.com/inodb/cbioportal-go/internal/clickhouse"
)

// LoadReference loads gene, gene_alias, type_of_cancer, reference_genome and
// reference_genome_gene from the gzipped seed dump into c's database. Returns
// (geneRows, aliasRows).
func LoadReference(c *ch.Client, gzPath string) (uint64, uint64, error) {
	if err := c.CreateDatabase(); err != nil {
		return 0, 0, err
	}
	ddls := []string{
		"CREATE TABLE IF NOT EXISTS `%s`.gene (entrez_gene_id Int64, hugo_gene_symbol String, genetic_entity_id Int64, type String) ENGINE = MergeTree ORDER BY hugo_gene_symbol",
		"CREATE TABLE IF NOT EXISTS `%s`.gene_alias (entrez_gene_id Int64, gene_alias String) ENGINE = MergeTree ORDER BY gene_alias",
		"CREATE TABLE IF NOT EXISTS `%s`.type_of_cancer (type_of_cancer_id String, name String, dedicated_color String, short_name String, parent String) ENGINE = MergeTree ORDER BY type_of_cancer_id",
		"CREATE TABLE IF NOT EXISTS `%s`.reference_genome (reference_genome_id Int32, species String, name String, build_name String, genome_size Nullable(Int64), url String, release_date Nullable(String)) ENGINE = MergeTree ORDER BY reference_genome_id",
		"CREATE TABLE IF NOT EXISTS `%s`.reference_genome_gene (entrez_gene_id Int64, reference_genome_id Int32, chr Nullable(String), cytoband Nullable(String), start Nullable(Int64), end Nullable(Int64)) ENGINE = MergeTree ORDER BY (reference_genome_id, entrez_gene_id)",
	}
	for _, d := range ddls {
		if _, err := c.Execute(fmt.Sprintf(d, c.Database())); err != nil {
			return 0, 0, err
		}
	}
	for _, t := range []string{"gene", "gene_alias", "type_of_cancer", "reference_genome", "reference_genome_gene"} {
		if _, err := c.Execute(fmt.Sprintf("TRUNCATE TABLE `%s`.%s", c.Database(), t)); err != nil {
			return 0, 0, err
		}
	}

	f, err := os.Open(gzPath)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return 0, 0, fmt.Errorf("opening seed gzip: %w", err)
	}
	defer gz.Close()
	r := bufio.NewReaderSize(gz, 1<<20)

	const (
		genePrefix   = "INSERT INTO `gene` VALUES "
		aliasPrefix  = "INSERT INTO `gene_alias` VALUES "
		cancerPrefix = "INSERT INTO `type_of_cancer` VALUES "
		genomePrefix = "INSERT INTO `reference_genome` VALUES "
		rggPrefix    = "INSERT INTO `reference_genome_gene` VALUES "
	)
	var geneRows, aliasRows uint64
	for {
		// mysqldump lines can be many MB; ReadString grows as needed.
		line, err := r.ReadString('\n')
		trimmed := strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(trimmed, genePrefix):
			n, e := insertValues(c, "gene", trimmed[len(genePrefix):])
			if e != nil {
				return 0, 0, e
			}
			geneRows += n
		case strings.HasPrefix(trimmed, aliasPrefix):
			n, e := insertValues(c, "gene_alias", trimmed[len(aliasPrefix):])
			if e != nil {
				return 0, 0, e
			}
			aliasRows += n
		case strings.HasPrefix(trimmed, cancerPrefix):
			if _, e := insertValues(c, "type_of_cancer", trimmed[len(cancerPrefix):]); e != nil {
				return 0, 0, e
			}
		case strings.HasPrefix(trimmed, genomePrefix):
			if _, e := insertValues(c, "reference_genome", trimmed[len(genomePrefix):]); e != nil {
				return 0, 0, e
			}
		case strings.HasPrefix(trimmed, rggPrefix):
			if _, e := insertValues(c, "reference_genome_gene", trimmed[len(rggPrefix):]); e != nil {
				return 0, 0, e
			}
		}
		if err != nil {
			break
		}
	}
	return geneRows, aliasRows, nil
}

func insertValues(c *ch.Client, table, body string) (uint64, error) {
	body = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(body), ";"))
	if body == "" {
		return 0, nil
	}
	sql := fmt.Sprintf("INSERT INTO `%s`.`%s` FORMAT Values", c.Database(), table)
	if err := c.InsertStream(sql, strings.NewReader(body)); err != nil {
		return 0, err
	}
	return uint64(strings.Count(body, "),(") + 1), nil
}
