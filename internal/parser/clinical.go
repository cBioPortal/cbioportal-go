package parser

import (
	"bufio"
	"os"
	"strings"
)

// ClinicalAttrMeta is one clinical attribute's metadata from a clinical file's
// `#`-prefixed header rows.
type ClinicalAttrMeta struct {
	AttrID           string
	DisplayName      string
	Description      string
	Datatype         string
	Priority         string
	PatientAttribute bool
}

// ParseAttributeMeta parses the metadata header of a clinical file. The rows,
// in order, are: `#`display-names, `#`descriptions, `#`datatypes, `#`priorities,
// (`#`attribute-types for MIXED), then the attribute-id header row.
// patientDefault is the attribute type for non-MIXED files. PATIENT_ID/SAMPLE_ID
// are relational keys and excluded.
func ParseAttributeMeta(path string, patientDefault bool) ([]ClinicalAttrMeta, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := bufio.NewReader(f)

	var metaRows [][]string
	var header []string
	for {
		line, err := r.ReadString('\n')
		if len(line) == 0 && err != nil {
			break
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(trimmed, "#") {
			metaRows = append(metaRows, strings.Split(trimmed[1:], "\t"))
		} else {
			header = strings.Split(trimmed, "\t")
			break
		}
		if err != nil {
			break
		}
	}

	get := func(row, col int) string {
		if row < len(metaRows) && col < len(metaRows[row]) {
			return strings.TrimSpace(metaRows[row][col])
		}
		return ""
	}
	hasTypes := len(metaRows) >= 5

	var out []ClinicalAttrMeta
	for i, rawID := range header {
		attrID := strings.ToUpper(strings.TrimSpace(rawID))
		if attrID == "" || attrID == "PATIENT_ID" || attrID == "SAMPLE_ID" {
			continue
		}
		patientAttr := patientDefault
		if hasTypes {
			patientAttr = strings.EqualFold(get(4, i), "PATIENT")
		}
		out = append(out, ClinicalAttrMeta{
			AttrID:           attrID,
			DisplayName:      firstNonEmpty(get(0, i), attrID),
			Description:      get(1, i),
			Datatype:         firstNonEmpty(get(2, i), "STRING"),
			Priority:         firstNonEmpty(get(3, i), "1"),
			PatientAttribute: patientAttr,
		})
	}
	return out, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
