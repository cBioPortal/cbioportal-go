package parser

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Layout is how a data file's rows are physically laid out.
type Layout int

const (
	// Record: one record per row (MAF, SV, SEG, clinical, timeline, panel matrix).
	Record Layout = iota
	// Headerless: one record per row, no header line (cancer type).
	Headerless
	// Matrix: gene/entity rows × one column per sample (CNA DISCRETE, expression…).
	Matrix
	// Other: not a genomic profile with tabular rows.
	Other
)

func (l Layout) String() string {
	switch l {
	case Record:
		return "Record"
	case Headerless:
		return "Headerless"
	case Matrix:
		return "Matrix"
	default:
		return "Other"
	}
}

// DataFile is a meta+data pair discovered in a study directory.
type DataFile struct {
	MetaPath           string
	DataPath           string
	AlterationType     string
	Datatype           string
	StableID           string
	ProfileName        string
	ProfileDescription string
	ShowInAnalysis     string
	GenericAssayType   string
	Layout             Layout
	StageSlug          string
}

// IsMolecularProfile reports whether this is a molecular genetic profile (has a
// stable id and a genomic alteration type) vs clinical/cancer-type/panel data.
func (d *DataFile) IsMolecularProfile() bool {
	if d.StableID == "" {
		return false
	}
	switch strings.ToUpper(d.AlterationType) {
	case "CLINICAL", "CANCER_TYPE", "GENE_PANEL_MATRIX":
		return false
	}
	return true
}

// Study is a discovered study: its identifier, meta_study fields, and files.
type Study struct {
	Dir     string
	StudyID string
	Meta    map[string]string
	Files   []DataFile
}

func classify(at, dt string) (Layout, string) {
	up := strings.ToUpper(at)
	udt := strings.ToUpper(dt)
	switch {
	case up == "CANCER_TYPE":
		return Headerless, "type_of_cancer"
	case up == "MUTATION_EXTENDED":
		return Record, "mutation_extended"
	case up == "MUTATION_UNCALLED":
		return Record, "mutation_uncalled"
	case up == "STRUCTURAL_VARIANT":
		return Record, "structural_variant"
	case up == "COPY_NUMBER_ALTERATION" && udt == "SEG":
		return Record, "cna_seg"
	case up == "COPY_NUMBER_ALTERATION" && udt == "DISCRETE_LONG":
		return Record, "cna_discrete_long"
	case up == "COPY_NUMBER_ALTERATION":
		return Matrix, "cna_matrix"
	case up == "CLINICAL" && udt == "PATIENT_ATTRIBUTES":
		return Record, "clinical_patient"
	case up == "CLINICAL" && udt == "SAMPLE_ATTRIBUTES":
		return Record, "clinical_sample"
	case up == "CLINICAL" && udt == "MIXED_ATTRIBUTES":
		return Record, "clinical_mixed"
	case up == "CLINICAL" && udt == "TIMELINE":
		return Record, "timeline"
	case up == "GENE_PANEL_MATRIX":
		return Record, "gene_panel_matrix"
	}
	switch up {
	case "MRNA_EXPRESSION", "MICRO_RNA_EXPRESSION", "METHYLATION", "PROTEIN_LEVEL",
		"PROTEIN_ARRAY_PROTEIN_LEVEL", "GENESET_SCORE", "GENERIC_ASSAY",
		"RNA_EXPRESSION", "PHOSPHORYLATION":
		return Matrix, strings.ToLower(up) + "_matrix"
	}
	return Other, strings.ToLower(up) + "_" + strings.ToLower(udt)
}

// Scan discovers a study directory: pairs meta_*.txt with data files, reads
// meta_study.txt, and classifies each data file.
func Scan(dir string) (*Study, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var metas []string
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, "meta_") && strings.HasSuffix(n, ".txt") {
			metas = append(metas, filepath.Join(dir, n))
		}
	}
	sort.Strings(metas)

	s := &Study{Dir: dir, Meta: map[string]string{}}
	for _, mp := range metas {
		meta, err := ReadMeta(mp)
		if err != nil {
			return nil, err
		}
		if s.StudyID == "" {
			if id := meta.Get("cancer_study_identifier"); id != "" {
				s.StudyID = id
			}
		}
		if filepath.Base(mp) == "meta_study.txt" {
			s.Meta = meta.Fields
		}
		at, dt, dataName := meta.Get("genetic_alteration_type"), meta.Get("datatype"), meta.Get("data_filename")
		if at == "" || dt == "" || dataName == "" {
			continue
		}
		dataPath := filepath.Join(dir, dataName)
		if _, err := os.Stat(dataPath); err != nil {
			continue
		}
		layout, slug := classify(at, dt)
		s.Files = append(s.Files, DataFile{
			MetaPath:           mp,
			DataPath:           dataPath,
			AlterationType:     at,
			Datatype:           dt,
			StableID:           meta.Get("stable_id"),
			ProfileName:        meta.Get("profile_name"),
			ProfileDescription: meta.Get("profile_description"),
			ShowInAnalysis:     meta.Get("show_profile_in_analysis_tab"),
			GenericAssayType:   meta.Get("generic_assay_type"),
			Layout:             layout,
			StageSlug:          slug,
		})
	}
	if s.StudyID == "" {
		s.StudyID = filepath.Base(dir)
	}
	return s, nil
}
