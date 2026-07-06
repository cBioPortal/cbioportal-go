package api

import "strings"

// alterationFilter is the study-view AlterationFilter: which mutation statuses
// (somatic/germline/unknown) and oncogenicities (driver/VUS/unknown, incl.
// custom tiers) to count. Bool pointers distinguish "not sent" (nil → include)
// from an explicit false.
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

// inc treats a nil (unsent) flag as "include".
func inc(b *bool) bool { return b == nil || *b }

// mutationFilterClause builds a SQL predicate over the `mutation` view columns
// (mutation_status, cbp_driver, cbp_driver_tiers) for the study-view alteration
// filter. Returns "" when nothing is excluded (fast path) or "1 = 0" when the
// filter excludes everything.
func (af *alterationFilter) mutationFilterClause() string {
	if af == nil {
		return ""
	}
	var clauses []string

	// Mutation status: somatic / germline / unknown.
	if !(inc(af.IncludeSomatic) && inc(af.IncludeGermline) && inc(af.IncludeUnknownStatus)) {
		var ors []string
		if inc(af.IncludeSomatic) {
			ors = append(ors, "lower(mutation_status) = 'somatic'")
		}
		if inc(af.IncludeGermline) {
			ors = append(ors, "lower(mutation_status) = 'germline'")
		}
		if inc(af.IncludeUnknownStatus) {
			ors = append(ors, "lower(mutation_status) NOT IN ('somatic', 'germline')")
		}
		if len(ors) == 0 {
			return "1 = 0"
		}
		clauses = append(clauses, "("+strings.Join(ors, " OR ")+")")
	}

	// Oncogenicity: a mutation is a DRIVER when the custom binary annotation says
	// so or its custom tier is enabled; UNKNOWN when no annotation has an opinion;
	// VUS otherwise (annotated non-driver).
	if !(inc(af.IncludeDriver) && inc(af.IncludeVUS) && inc(af.IncludeUnknownOncogenicity)) {
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
			return "1 = 0"
		}
		clauses = append(clauses, "("+strings.Join(ors, " OR ")+")")
	}

	return strings.Join(clauses, " AND ")
}

// and joins a base scope with an optional extra predicate.
func andClause(base, extra string) string {
	if extra == "" {
		return base
	}
	return base + " AND " + extra
}
