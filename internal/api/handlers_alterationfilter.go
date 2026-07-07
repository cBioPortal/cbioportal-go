package api

import (
	"fmt"
	"strings"
)

// alterationFilter is the study-view AlterationFilter: which mutation/SV
// statuses (somatic/germline/unknown) and oncogenicities (driver/VUS/unknown,
// incl. custom tiers) to count. Bool pointers distinguish "not sent" (nil →
// include) from an explicit false.
type alterationFilter struct {
	IncludeDriver              *bool           `json:"includeDriver"`
	IncludeVUS                 *bool           `json:"includeVUS"`
	IncludeUnknownOncogenicity *bool           `json:"includeUnknownOncogenicity"`
	IncludeGermline            *bool           `json:"includeGermline"`
	IncludeSomatic             *bool           `json:"includeSomatic"`
	IncludeUnknownStatus       *bool           `json:"includeUnknownStatus"`
	IncludeUnknownTier         *bool           `json:"includeUnknownTier"`
	TiersBooleanMap            map[string]bool `json:"tiersBooleanMap"`
}

const falseClause = "1 = 0"

// inc treats a nil (unsent) flag as "include".
func inc(b *bool) bool { return b == nil || *b }

// statusClause builds the somatic/germline/unknown predicate over the given
// status column (mutation_status for mutations, sv_status for SVs). Returns ""
// when all statuses are included, falseClause when none are.
func (af *alterationFilter) statusClause(col string) string {
	if af == nil || (inc(af.IncludeSomatic) && inc(af.IncludeGermline) && inc(af.IncludeUnknownStatus)) {
		return ""
	}
	var ors []string
	if inc(af.IncludeSomatic) {
		ors = append(ors, fmt.Sprintf("lower(%s) = 'somatic'", col))
	}
	if inc(af.IncludeGermline) {
		ors = append(ors, fmt.Sprintf("lower(%s) = 'germline'", col))
	}
	if inc(af.IncludeUnknownStatus) {
		ors = append(ors, fmt.Sprintf("lower(%s) NOT IN ('somatic', 'germline')", col))
	}
	if len(ors) == 0 {
		return falseClause
	}
	return "(" + strings.Join(ors, " OR ") + ")"
}

// oncogenicityClause builds the driver/VUS/unknown predicate over cbp_driver /
// cbp_driver_tiers, shared by mutation, CNA and SV filters. DRIVER when the
// custom binary annotation says so or a custom tier is enabled; UNKNOWN when no
// annotation has an opinion; VUS otherwise. "" when all three included.
func (af *alterationFilter) oncogenicityClause() string {
	if af == nil || (inc(af.IncludeDriver) && inc(af.IncludeVUS) && inc(af.IncludeUnknownOncogenicity)) {
		return ""
	}
	var tiers []string
	for t, on := range af.TiersBooleanMap {
		if on {
			tiers = append(tiers, t)
		}
	}
	driverExpr := "cbp_driver = 'Putative_Driver'"
	if len(tiers) > 0 {
		driverExpr = "(" + driverExpr + " OR cbp_driver_tiers IN (" + inList(tiers) + "))"
	}
	unknownExpr := "(cbp_driver IN ('', 'NA', 'Unknown') AND cbp_driver_tiers IN ('', 'NA'))"
	var ors []string
	if inc(af.IncludeDriver) {
		ors = append(ors, driverExpr)
	}
	if inc(af.IncludeVUS) {
		ors = append(ors, "(NOT "+driverExpr+" AND NOT "+unknownExpr+")")
	}
	if inc(af.IncludeUnknownOncogenicity) {
		ors = append(ors, "(NOT "+driverExpr+" AND "+unknownExpr+")")
	}
	if len(ors) == 0 {
		return falseClause
	}
	return "(" + strings.Join(ors, " OR ") + ")"
}

// combine ANDs the status (over statusCol) and oncogenicity predicates; returns
// "" for no filter or falseClause if either excludes everything.
func (af *alterationFilter) combine(statusCol string) string {
	if af == nil {
		return ""
	}
	var clauses []string
	for _, c := range []string{af.statusClause(statusCol), af.oncogenicityClause()} {
		if c == falseClause {
			return falseClause
		}
		if c != "" {
			clauses = append(clauses, c)
		}
	}
	return strings.Join(clauses, " AND ")
}

// mutationFilterClause filters mutation gene counts (status + oncogenicity).
func (af *alterationFilter) mutationFilterClause() string { return af.combine("mutation_status") }

// svFilterClause filters SV gene counts (sv_status + oncogenicity).
func (af *alterationFilter) svFilterClause() string { return af.combine("sv_status") }

// cnaFilterClause filters CNA gene counts (oncogenicity only; CNA has no status).
func (af *alterationFilter) cnaFilterClause() string {
	if af == nil {
		return ""
	}
	if onc := af.oncogenicityClause(); onc != falseClause {
		return onc
	}
	return falseClause
}

// andClause joins a base scope with an optional extra predicate.
func andClause(base, extra string) string {
	if extra == "" {
		return base
	}
	return base + " AND " + extra
}
