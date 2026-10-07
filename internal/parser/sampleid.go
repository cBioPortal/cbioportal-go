package parser

import (
	"fmt"
	"regexp"
	"strings"
)

// tcgaSample is the sample part of a TCGA barcode (project, tissue source
// site, participant, sample type), as StableIdUtil.TCGA_SAMPLE_BARCODE_REGEX.
var tcgaSample = regexp.MustCompile(`^(TCGA-\w\w-\w\w\w\w-\d\d)`)

// SampleID is cbioportal-core's StableIdUtil.getSampleId, which the
// importers apply to the sample ids of clinical sample, mutation, matrix,
// segment, discrete CNA long, gene panel matrix and case list files: a TCGA
// barcode is cut to its sample part ("TCGA-RZ-AB0B-01A-11D" ->
// "TCGA-RZ-AB0B-01"), with "Tumor" read as "01" and "Normal" as "11", and a
// barcode of fewer than four parts gets "-01". Other ids stay as they are.
func SampleID(id string) string {
	if !strings.HasPrefix(id, "TCGA") {
		return id
	}
	clean := id
	if strings.Contains(id, "Tumor") {
		clean = strings.ReplaceAll(id, "Tumor", "01")
	} else if strings.Contains(id, "Normal") {
		clean = strings.ReplaceAll(id, "Normal", "11")
	}
	parts := strings.Split(clean, "-")
	if len(parts) < 4 {
		return id + "-01"
	}
	short := strings.Join(parts[:4], "-")
	if m := tcgaSample.FindStringSubmatch(short); m != nil {
		return m[1]
	}
	return short
}

// SampleIDSQL is SampleID as a ClickHouse expression over a String
// expression.
func SampleIDSQL(x string) string {
	clean := fmt.Sprintf("if(position(%[1]s, 'Tumor') > 0, replaceAll(%[1]s, 'Tumor', '01'), replaceAll(%[1]s, 'Normal', '11'))", x)
	short := fmt.Sprintf("arrayStringConcat(arraySlice(splitByChar('-', %s), 1, 4), '-')", clean)
	sample := fmt.Sprintf("extract(%s, '^(TCGA-\\\\w\\\\w-\\\\w\\\\w\\\\w\\\\w-\\\\d\\\\d)')", short)
	return fmt.Sprintf("if(startsWith(%[1]s, 'TCGA'), if(length(splitByChar('-', %[2]s)) < 4, concat(%[1]s, '-01'), "+
		"if(%[3]s != '', %[3]s, %[4]s)), %[1]s)", x, clean, sample, short)
}
