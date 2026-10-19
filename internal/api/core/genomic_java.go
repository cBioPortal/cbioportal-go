package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Value conventions of the cBioPortal importer (cbioportal-core with the maf
// library's TabDelimitedFileUtil) and of the Java web layer, applied to the
// raw staged columns so API records carry the values Java stores and returns.

// NAString is TabDelimitedFileUtil.getPartString: an empty or absent column
// is stored as "NA".
func NAString(s string) string {
	if s == "" {
		return "NA"
	}
	return s
}

// JavaPartInt is TabDelimitedFileUtil.getPartInt: (int) Double.parseDouble of
// the cell, or -1 when it is empty or not a number.
func JavaPartInt(s string) int64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return -1
	}
	switch {
	case math.IsNaN(f):
		return 0
	case f >= math.MaxInt32:
		return math.MaxInt32
	case f <= math.MinInt32:
		return math.MinInt32
	}
	return int64(f)
}

// JavaCount is a MAF read count as ExtendedMutationUtil resolves it: the
// parsed integer, or null when the cell is empty / not a number (-1).
func JavaCount(s string) *int64 {
	if v := JavaPartInt(s); v != -1 {
		return &v
	}
	return nil
}

// NamespaceColumnsJSON is the annotation JSON of a record: absent (null) when
// the record has no custom-namespace values, as NamespaceColumnParser stores
// null for an empty namespace map. Null map entries are left out, as Jackson's
// NON_NULL inclusion drops them from Maps (a namespace whose columns are all
// empty is written as {}).
func NamespaceColumnsJSON(s string) json.RawMessage {
	s = strings.TrimSpace(s)
	if s == "" || s == "{}" || s == "null" || !json.Valid([]byte(s)) {
		return nil
	}
	if !strings.Contains(s, "null") {
		return json.RawMessage(s)
	}
	var b bytes.Buffer
	if err := writeWithoutNullEntries(&b, json.NewDecoder(strings.NewReader(s))); err != nil {
		return json.RawMessage(s)
	}
	return json.RawMessage(b.Bytes())
}

// writeWithoutNullEntries copies the next JSON value from dec to b, keeping
// key order and leaving out object members whose value is null.
func writeWithoutNullEntries(b *bytes.Buffer, dec *json.Decoder) error {
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			b.WriteByte('{')
			first := true
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return err
				}
				var raw json.RawMessage
				if err := dec.Decode(&raw); err != nil {
					return err
				}
				if string(bytes.TrimSpace(raw)) == "null" {
					continue
				}
				if !first {
					b.WriteByte(',')
				}
				first = false
				if err := writeJSONToken(b, keyTok); err != nil {
					return err
				}
				b.WriteByte(':')
				if err := writeWithoutNullEntries(b, json.NewDecoder(bytes.NewReader(raw))); err != nil {
					return err
				}
			}
			b.WriteByte('}')
		case '[':
			b.WriteByte('[')
			for i := 0; dec.More(); i++ {
				var raw json.RawMessage
				if err := dec.Decode(&raw); err != nil {
					return err
				}
				if i > 0 {
					b.WriteByte(',')
				}
				if err := writeWithoutNullEntries(b, json.NewDecoder(bytes.NewReader(raw))); err != nil {
					return err
				}
			}
			b.WriteByte(']')
		}
		_, err = dec.Token()
		return err
	default:
		return writeJSONToken(b, t)
	}
}

// writeJSONToken writes a scalar token without HTML escaping.
func writeJSONToken(b *bytes.Buffer, tok json.Token) error {
	enc := json.NewEncoder(b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(tok); err != nil {
		return err
	}
	b.Truncate(b.Len() - 1) // Encode's newline
	return nil
}

// driverAnnotation holds the alteration_driver_annotation columns of a
// record. The importer writes that row only when the driver filter or the
// driver tiers filter has a value other than empty / NA; without the row all
// four fields are null.
type driverAnnotation struct {
	Filter, FilterAnn, TiersFilter, TiersFilterAnn *string
}

// NewDriverAnnotation returns the driver annotation columns of an alteration
// (nil where not given or NA); fieldValue reads the record's columns.
func NewDriverAnnotation(filter, filterAnn, tiers, tiersAnn string, fieldValue func(string) string) driverAnnotation {
	given := func(v string) bool { return v != "" && !strings.EqualFold(v, "NA") }
	if !given(filter) && !given(tiers) {
		return driverAnnotation{}
	}
	ptr := func(v string) *string { v = fieldValue(v); return &v }
	return driverAnnotation{ptr(filter), ptr(filterAnn), ptr(tiers), ptr(tiersAnn)}
}

// MAFDriverFilter is how MafUtil reads the driver filter columns
// (getPartStringAllowEmptyAndNA): trimmed, with NA read as empty.
func MAFDriverFilter(v string) string {
	if strings.EqualFold(strings.TrimSpace(v), "NA") {
		return ""
	}
	return strings.TrimSpace(v)
}

// ---------- Numeric molecular values ----------

// JavaTruncatedValueSQL is MolecularDataMapper's getTruncatedGeneticAlterationValues
// for one cell x: non-numeric values unchanged, zero as '0', magnitudes below
// 1e-4 as a 4-significant-digit mantissa with an exponent, others rounded to
// 4 decimals.
func JavaTruncatedValueSQL(x string) string {
	f := "toFloat64OrZero(" + x + ")"
	return "multiIf(isNull(accurateCastOrNull(" + x + ", 'Float64')), " + x + ", " +
		f + " = 0, '0', " +
		"abs(" + f + ") < 0.0001, concat(toString(round(" + f + " / pow(10, floor(log10(abs(" + f + ")))), 3)), 'e', " +
		"toString(floor(log10(abs(" + f + "))))), " +
		"toString(round(" + f + ", 4)))"
}

var javaDecimalRE = regexp.MustCompile(`^([+-]?)([0-9]*)(?:\.([0-9]*))?(?:[eE]([+-]?[0-9]+))?$`)

// JavaNumericSQL is the SQL condition JavaDecimal accepts x on: a decimal
// with at least one mantissa digit and an exponent in the int range.
func JavaNumericSQL(x string) string {
	digits := fmt.Sprintf("extract(%s, '[eE][+-]?0*([0-9]+)$')", x)
	return fmt.Sprintf("(match(%[1]s, '^[+-]?([0-9]+([.][0-9]*)?|[.][0-9]+)([eE][+-]?[0-9]+)?$') AND "+
		"(NOT match(%[1]s, '[eE]') OR (length(%[2]s) <= 10 AND toInt64OrZero(%[2]s) <= "+
		"if(extract(%[1]s, '[eE]([+-]?)') = '-', 2147483648, 2147483647))))", x, digits)
}

// JavaDecimal parses s as MolecularDataController.filterNonNumberMolecularData
// does (NumberUtils.isNumber, then new BigDecimal) and renders it the way
// Jackson writes that BigDecimal (BigDecimal.toString). ok is false for a
// value Java drops as non-numeric.
func JavaDecimal(s string) (json.Number, bool) {
	m := javaDecimalRE.FindStringSubmatch(s)
	if m == nil || m[2]+m[3] == "" {
		return "", false
	}
	exp := int64(0)
	if m[4] != "" {
		e, err := strconv.ParseInt(m[4], 10, 32)
		if err != nil {
			return "", false
		}
		exp = e
	}
	coeff := strings.TrimLeft(m[2]+m[3], "0")
	if coeff == "" {
		coeff = "0"
	}
	scale := int64(len(m[3])) - exp
	var b strings.Builder
	if m[1] == "-" && coeff != "0" {
		b.WriteByte('-')
	}
	adjusted := -scale + int64(len(coeff)-1)
	switch {
	case scale == 0:
		b.WriteString(coeff)
	case scale > 0 && adjusted >= -6:
		if n := int64(len(coeff)); n > scale {
			b.WriteString(coeff[:n-scale])
			b.WriteByte('.')
			b.WriteString(coeff[n-scale:])
		} else {
			b.WriteString("0.")
			b.WriteString(strings.Repeat("0", int(scale-n)))
			b.WriteString(coeff)
		}
	default:
		b.WriteString(coeff[:1])
		if len(coeff) > 1 {
			b.WriteByte('.')
			b.WriteString(coeff[1:])
		}
		b.WriteByte('E')
		if adjusted > 0 {
			b.WriteByte('+')
		}
		b.WriteString(strconv.FormatInt(adjusted, 10))
	}
	return json.Number(b.String()), true
}

// ---------- Protein positions (ExtendedMutationUtil) ----------

// JavaSplit is String.split for a literal separator: trailing empty strings
// are removed.
func JavaSplit(s, sep string) []string {
	parts := strings.Split(s, sep)
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

func partIntAt(parts []string, i int) int64 {
	if i >= len(parts) {
		return -1
	}
	return JavaPartInt(parts[i])
}

// JavaProteinPositions is ExtendedMutationUtil.getProteinPosStart / End:
// the start (and end) of the MAF Protein_position column ("600/766",
// "600-601/766"), falling back to positions parsed from the protein change;
// -1 when neither yields one.
func JavaProteinPositions(proteinPosition, proteinChange string) (int64, int64) {
	var range0 []string
	if parts := JavaSplit(NAString(proteinPosition), "/"); len(parts) > 0 {
		range0 = JavaSplit(parts[0], "-")
	}
	start, end := partIntAt(range0, 0), partIntAt(range0, 1)
	if start == -1 || end == -1 {
		aStart, aEnd := annotateProteinChange(proteinChange)
		if start == -1 {
			start = aStart
		}
		if end == -1 {
			end = aEnd
		}
	}
	return start, end
}

var (
	pcPointRE     = regexp.MustCompile(`^([A-Z*]+)([0-9]+)([A-Z*?]*)$`)
	pcInsRE       = regexp.MustCompile(`^[A-Z]?([0-9]+)(_[A-Z]?([0-9]+))?(delins|ins)([A-Z]+)$`)
	pcSpliceRE    = regexp.MustCompile(`^[A-Z]?([0-9]+)(_[A-Z]?([0-9]+))?(_)?splice$`)
	pcRangeRE     = regexp.MustCompile(`^[A-Z]?([0-9]+)_[A-Z]?([0-9]+)(.+)$`)
	pcFrameRE     = regexp.MustCompile(`^([A-Z*])([0-9]+)[A-Z]?fs.*$`)
	pcInDelDupRE  = regexp.MustCompile(`^([A-Z]+)?([0-9]+)((ins)|(del)|(dup))$`)
	javaIntDigits = func(s string) int64 { v, _ := strconv.ParseInt(s, 10, 32); return v }
)

// annotateProteinChange is ExtendedMutationUtil.annotateProteinChange.
func annotateProteinChange(pc string) (int64, int64) {
	pc = strings.TrimPrefix(pc, "p.")
	if i := strings.Index(pc, "["); i >= 0 {
		pc = pc[:i]
	}
	pc = strings.TrimSpace(pc)
	if m := pcPointRE.FindStringSubmatch(pc); m != nil {
		ref, v := m[1], m[3]
		start := javaIntDigits(m[2])
		if ref == v || ref == "*" || v == "*" || start == 1 || v == "?" {
			return start, start
		}
		return start, start + int64(len(ref)) - 1
	}
	for _, re := range []*regexp.Regexp{pcInsRE, pcSpliceRE} {
		if m := re.FindStringSubmatch(pc); m != nil {
			start := javaIntDigits(m[1])
			if m[3] != "" {
				return start, javaIntDigits(m[3])
			}
			return start, start
		}
	}
	if m := pcRangeRE.FindStringSubmatch(pc); m != nil {
		return javaIntDigits(m[1]), javaIntDigits(m[2])
	}
	if m := pcFrameRE.FindStringSubmatch(pc); m != nil {
		s := javaIntDigits(m[2])
		return s, s
	}
	if m := pcInDelDupRE.FindStringSubmatch(pc); m != nil {
		s := javaIntDigits(m[2])
		return s, s
	}
	return -1, -1
}

// ---------- Mutation keyword (MutationKeywordUtils) ----------

// MutationKeyword is the keyword the importer stores on a mutation event:
// "<HUGO> " + MutationKeywordUtils.guessOncotatorMutationKeyword, or "" (null)
// when no keyword applies.
func MutationKeyword(hugo, proteinChange, mutationType string) string {
	kw, ok := guessOncotatorMutationKeyword(proteinChange, mutationType)
	if !ok {
		return ""
	}
	return strings.ToUpper(hugo) + " " + kw
}

func full(pattern string) *regexp.Regexp { return regexp.MustCompile(`^(?:` + pattern + `)$`) }

var (
	kwMissenseNonStart  = full(`M1[A-LN-Z][A-Z]*`)
	kwMissensePoint     = full(`([A-Z][0-9]+)[A-Z]`)
	kwMissenseRange     = full(`([0-9]+)_([0-9]+)[A-Z]+>[A-Z]+`)
	kwInsStop           = full(`[0-9]+_[0-9]+[A-Z]*((ins)|(>)).*\*.*`)
	kwInsStart1         = full(`1_1M>?[A-LN-Z][A-Z]*`)
	kwInsStart2         = full(`1_2ins[A-LN-Z][A-Z]*`)
	kwInsFind           = regexp.MustCompile(`([0-9]+)_([0-9]+)[A-Z]*((ins)|(>))[A-Z]+`)
	kwDelM1a            = full(`M[A-Z]*1del`)
	kwDelM1b            = full(`1_[0-9]+M[A-Z]*>[A-LN-Z][A-Z]*`)
	kwDelStopA          = full(`[A-Z]*\*[0-9]+del`)
	kwDelStopB          = full(`[0-9]+_[0-9]+[A-Z]*\*>[A-Z]+`)
	kwDelStopC          = full(`[0-9]+_[0-9]+[A-Z]*>.*\*.*`)
	kwDelResidues       = full(`([A-Z]+)([0-9]+)del`)
	kwDelRangeFind      = regexp.MustCompile(`([0-9]+)_([0-9]+)[A-Z]*>[A-Z]+`)
	kwSilentFind        = regexp.MustCompile(`([0-9]+)`)
	cosmicTruncating    = []*regexp.Regexp{full(`\(?[A-Z*]?[0-9]+[A-Z*]?\)?fs\*?>?\??[0-9]*`), full(`[A-Z][0-9]+>?\*`), full(`M1>?[A-LN-Z]`), full(`M1delM`), full(`M1_M1ins[A-LN-Z][A-Z]*`), full(`([A-Z][0-9]+_)?\*[0-9]+>?[A-Z]`), full(`([A-Z][0-9]+_)\*[0-9]+del([A-Z]+\*)?`), full(`\*[0-9]+del\*`), full(`[A-Z][0-9]+(_[A-Z][0-9]+)?((ins)|(>)).*\*.*`), full(`[A-Z][0-9]+_[A-Z][0-9]+>.*\*.*`), full(`M1_[A-Z][0-9]+>[A-LN-Z][A-Z]*`)}
	cosmicSilentHead    = full(`(([A-Z*])[0-9]+)>?(.*)`)
	cosmicSubstitution  = full(`([A-Z]([0-9]+))>?([A-Z]+)`)
	cosmicInsertions    = []*regexp.Regexp{full(`[A-Z]?([0-9]+)_[A-Z*]?[0-9]+ins(([A-Z]+)|([0-9]+))`), full(`\*([0-9]+)[A-Z]?ext\*(([0-9]+)|(\?))`), full(`[A-Z*]([0-9]+)(_[A-Z*][0-9]+)?dup`), full(`[A-Z]?([0-9]+)>[A-Z][A-Z]+`), full(`\*([0-9]+)_\*[0-9]+ins[A-Z]+\*?`)}
	cosmicDelOne        = full(`[A-Z]([0-9]+)del[A-Z]?`)
	cosmicDelRange      = full(`[A-Z]([0-9]+)_[A-Z]([0-9]+)del(([A-Z]+)|([0-9]+))?`)
	cosmicMultiResidue  = full(`[A-Z]([0-9]+)_[A-Z*]([0-9]+)((>)|(delins))([A-Z*]+)`)
	cosmicDelinsOne     = full(`[A-Z*]([0-9]+)delins[A-Z*]+[0-9]*`)
	cosmicDelext        = full(`([A-Z*]+)([0-9]+)delext\*([0-9]+)`)
	javaInt             = func(s string) (int64, bool) { v, err := strconv.ParseInt(s, 10, 32); return v, err == nil }
	javaIntString       = func(v int64) string { return strconv.FormatInt(v, 10) }
	silentSameResidueOK = func(ref, rest string) bool { return rest == ref || rest == "=" || rest == "%3D" }
)

// guessOncotatorMutationKeyword is MutationKeywordUtils.guessOncotatorMutationKeyword;
// ok is false where Java returns null.
func guessOncotatorMutationKeyword(aa, mt string) (string, bool) {
	if mt == "Nonsense_Mutation" || mt == "Splice_Site" || strings.HasPrefix(mt, "Frame_Shift_") ||
		mt == "Nonstop_Mutation" || mt == "Translation_Start_Site" {
		return "truncating", true
	}
	switch mt {
	case "Missense_Mutation":
		if kwMissenseNonStart.MatchString(aa) {
			return "truncating", true
		}
		if m := kwMissensePoint.FindStringSubmatch(aa); m != nil {
			return m[1] + " missense", true
		}
		if m := kwMissenseRange.FindStringSubmatch(aa); m != nil {
			return m[1] + "-" + m[2] + " missense", true
		}
	case "In_Frame_Ins":
		if kwInsStop.MatchString(aa) || kwInsStart1.MatchString(aa) || kwInsStart2.MatchString(aa) {
			return "truncating", true
		}
		if m := kwInsFind.FindStringSubmatch(aa); m != nil {
			return m[1] + " insertion", true
		}
	case "In_Frame_Del":
		if kwDelM1a.MatchString(aa) || kwDelM1b.MatchString(aa) || kwDelStopA.MatchString(aa) ||
			kwDelStopB.MatchString(aa) || kwDelStopC.MatchString(aa) {
			return "truncating", true
		}
		if m := kwDelResidues.FindStringSubmatch(aa); m != nil {
			if p, ok := javaInt(m[2]); ok {
				return m[2] + "-" + javaIntString(p+int64(len(m[1]))-1) + " deletion", true
			}
			return "", false
		}
		if m := kwDelRangeFind.FindStringSubmatch(aa); m != nil {
			return m[1] + "-" + m[2] + " deletion", true
		}
	case "Silent":
		if m := kwSilentFind.FindStringSubmatch(aa); m != nil {
			return m[1] + "silent", true
		}
	case "Exon skipping", "vIII deletion":
		return aa, true
	case "NA":
		return guessCosmicKeyword(aa)
	}
	return "", false
}

// guessCosmicKeyword is MutationKeywordUtils.guessCosmicKeyword.
func guessCosmicKeyword(aa string) (string, bool) {
	if aa == "?fs" {
		return "truncating", true
	}
	for _, re := range cosmicTruncating {
		if re.MatchString(aa) {
			return "truncating", true
		}
	}
	if m := cosmicSilentHead.FindStringSubmatch(aa); m != nil && silentSameResidueOK(m[2], m[3]) {
		return m[1] + " silent", true
	}
	if m := cosmicSubstitution.FindStringSubmatch(aa); m != nil {
		if len(m[3]) > 1 {
			return m[2] + " insertion", true
		}
		return m[1] + " missense", true
	}
	for _, re := range cosmicInsertions {
		if m := re.FindStringSubmatch(aa); m != nil {
			return m[1] + " insertion", true
		}
	}
	if m := cosmicDelOne.FindStringSubmatch(aa); m != nil {
		return m[1] + "-" + m[1] + " deletion", true
	}
	if m := cosmicDelRange.FindStringSubmatch(aa); m != nil {
		return m[1] + "-" + m[2] + " deletion", true
	}
	if m := cosmicMultiResidue.FindStringSubmatch(aa); m != nil {
		a, okA := javaInt(m[1])
		b, okB := javaInt(m[2])
		if !okA || !okB {
			return "", false
		}
		n1, n2 := b-a+1, int64(len(m[6]))
		switch {
		case n1 == n2:
			return m[1] + "-" + m[2] + " missense", true
		case n1 > n2:
			return m[1] + "-" + m[2] + " deletion", true
		}
		return m[1] + " insertion", true
	}
	if m := cosmicDelinsOne.FindStringSubmatch(aa); m != nil {
		return m[1] + " insertion", true
	}
	if m := cosmicDelext.FindStringSubmatch(aa); m != nil {
		n2, ok2 := javaInt(m[3])
		n3, ok3 := javaInt(m[2])
		if ok2 && ok3 {
			n1 := int64(len(m[1]))
			if n1 == n2 {
				return m[2] + "-" + javaIntString(n3+n2-1) + " missense", true
			}
			if n1 < n2 {
				return m[2] + " insertion", true
			}
		}
	}
	return "", false
}
