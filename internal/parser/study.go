package parser

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// metaFileName matches meta file names as cbioportal-core's importer
// (get_meta_filenames) does: "meta" after a word start or "_", followed by a
// word end, "_" or a digit, in any case.
var metaFileName = regexp.MustCompile(`(?i)(\b|_)meta(\b|[_0-9])`)

// IsMetaFile reports whether a study directory entry name is a meta file.
func IsMetaFile(name string) bool {
	return metaFileName.MatchString(name) && !strings.HasPrefix(name, ".") && !strings.HasSuffix(name, "~")
}

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
	Namespaces         string
	GenePanel          string
	PatientLevel       string
	PivotThreshold     string
	SortOrder          string
	// VariantClassificationFilter is the MAF meta file's
	// variant_classification_filter (the Variant_Classification values the
	// importer leaves out) as a JSON array, or empty when the file has no
	// such key and the default filter applies.
	VariantClassificationFilter string
	// EntityMetaProperties are the meta file's generic_entity_meta_properties:
	// the generic assay property columns, named as listed.
	EntityMetaProperties []string
	Layout               Layout
	StageSlug            string
	// ImportSeq is the meta file's position in directory order, the order in
	// which cbioportal-core's importer takes files of the same kind.
	ImportSeq int
}

// MatrixShape splits the file's header into identifier and sample columns;
// a generic assay file's listed property columns are identifiers too.
func (d *DataFile) MatrixShape(columns []string) MatrixShape {
	return ShapeWithFeatures(columns, d.EntityMetaProperties)
}

// IsPatientLevel reports whether a generic assay file's columns are patient
// ids (patient_level: true in its meta file).
func (d *DataFile) IsPatientLevel() bool {
	return strings.EqualFold(strings.TrimSpace(d.PatientLevel), "true")
}

// IsMolecularProfile reports whether this is a molecular genetic profile (has a
// stable id and a genomic alteration type) vs clinical/cancer-type/panel data.
// Segment files are not, even with a stable_id in their meta file:
// ImportCopyNumberSegmentData stores no genetic profile.
func (d *DataFile) IsMolecularProfile() bool {
	if d.StableID == "" || strings.EqualFold(d.Datatype, "SEG") {
		return false
	}
	switch strings.ToUpper(d.AlterationType) {
	case "CLINICAL", "CANCER_TYPE", "GENE_PANEL_MATRIX", "GISTIC_GENES_AMP", "GISTIC_GENES_DEL", "MUTSIG", "RESOURCE":
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
	case up == "GISTIC_GENES_AMP" || up == "GISTIC_GENES_DEL":
		return Record, "gistic"
	case up == "MUTSIG":
		// Header names collide case-insensitively (N vs n), so MutSig is
		// staged by a dedicated reader rather than schema-on-read.
		return Other, "mutsig"
	}
	switch up {
	case "MRNA_EXPRESSION", "MICRO_RNA_EXPRESSION", "METHYLATION", "PROTEIN_LEVEL",
		"PROTEIN_ARRAY_PROTEIN_LEVEL", "GENESET_SCORE", "GENERIC_ASSAY",
		"RNA_EXPRESSION", "PHOSPHORYLATION":
		return Matrix, strings.ToLower(up) + "_matrix"
	}
	return Other, strings.ToLower(up) + "_" + strings.ToLower(udt)
}

// Scan discovers a study directory: pairs meta files with data files, reads
// meta_study.txt, and classifies each data file.
func Scan(dir string) (*Study, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var metas []string
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() && IsMetaFile(n) {
			metas = append(metas, filepath.Join(dir, n))
		}
	}
	sort.Strings(metas)
	seq := DirOrder(dir)

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
		if rt := meta.Get("resource_type"); at == "" && rt != "" && dataName != "" {
			// Resource files (meta_resource_*) carry resource_type instead of
			// genetic_alteration_type/datatype.
			dataPath := filepath.Join(dir, dataName)
			if _, err := os.Stat(dataPath); err == nil {
				s.Files = append(s.Files, DataFile{
					MetaPath: mp, DataPath: dataPath, AlterationType: "RESOURCE", Datatype: strings.ToUpper(rt),
					Layout: Record, StageSlug: "resource_" + strings.ToLower(rt), ImportSeq: seq[filepath.Base(mp)],
				})
			}
			continue
		}
		if at == "" || dt == "" || dataName == "" {
			continue
		}
		dataPath := filepath.Join(dir, dataName)
		if _, err := os.Stat(dataPath); err != nil {
			continue
		}
		layout, slug := classify(at, dt)
		s.Files = append(s.Files, DataFile{
			MetaPath:                    mp,
			DataPath:                    dataPath,
			AlterationType:              at,
			Datatype:                    dt,
			StableID:                    meta.Get("stable_id"),
			ProfileName:                 meta.Get("profile_name"),
			ProfileDescription:          meta.Get("profile_description"),
			ShowInAnalysis:              meta.Get("show_profile_in_analysis_tab"),
			GenericAssayType:            meta.Get("generic_assay_type"),
			Namespaces:                  meta.Get("namespaces"),
			GenePanel:                   meta.Get("gene_panel"),
			PatientLevel:                meta.Get("patient_level"),
			PivotThreshold:              meta.Get("pivot_threshold_value"),
			SortOrder:                   meta.Get("value_sort_order"),
			VariantClassificationFilter: variantClassificationFilter(meta),
			EntityMetaProperties:        splitList(meta.Get("generic_entity_meta_properties")),
			Layout:                      layout,
			StageSlug:                   slug,
			ImportSeq:                   seq[filepath.Base(mp)],
		})
	}
	if s.StudyID == "" {
		s.StudyID = filepath.Base(dir)
	}
	return s, nil
}

// DirOrder maps the entries of a directory to their position in directory
// order, unsorted, as Python's os.listdir returns them: cbioportal-core's
// importer takes a study's meta files and case lists in that order.
func DirOrder(dir string) map[string]int {
	f, err := os.Open(dir)
	if err != nil {
		return nil
	}
	defer f.Close()
	names, _ := f.Readdirnames(-1)
	out := make(map[string]int, len(names))
	for i, n := range names {
		out[n] = i
	}
	return out
}

// splitList splits a comma-separated meta value, dropping empty items.
func splitList(v string) []string {
	var out []string
	for _, x := range strings.Split(strings.TrimSpace(v), ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// variantClassificationFilter reads a meta file's
// variant_classification_filter as GeneticProfileReader does: the value
// split on commas (Java's String.split, which drops trailing empty parts),
// each part trimmed. The result is a JSON array, or empty without the key.
func variantClassificationFilter(meta *MetaFile) string {
	v, ok := meta.Fields["variant_classification_filter"]
	if !ok {
		return ""
	}
	parts := strings.Split(v, ",")
	for len(parts) > 1 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	if v != "" && len(parts) == 1 && parts[0] == "" {
		parts = nil
	}
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	b, _ := json.Marshal(append([]string{}, parts...))
	return string(b)
}
