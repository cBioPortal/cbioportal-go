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

// oncogenicityClause is the BINARY custom-driver dimension over cbp_driver only:
// DRIVER = Putative_Driver, VUS = Putative_Passenger, UNKNOWN = neither. It is
// independent of the tier dimension (the "Putative drivers" checkbox toggles
// only this, not tier-annotated events). "" when all three are included.
func (af *alterationFilter) oncogenicityClause() string {
	if af == nil || (inc(af.IncludeDriver) && inc(af.IncludeVUS) && inc(af.IncludeUnknownOncogenicity)) {
		return ""
	}
	var ors []string
	if inc(af.IncludeDriver) {
		ors = append(ors, "cbp_driver = 'Putative_Driver'")
	}
	if inc(af.IncludeVUS) {
		ors = append(ors, "cbp_driver = 'Putative_Passenger'")
	}
	if inc(af.IncludeUnknownOncogenicity) {
		ors = append(ors, "cbp_driver NOT IN ('Putative_Driver', 'Putative_Passenger')")
	}
	if len(ors) == 0 {
		return falseClause
	}
	return "(" + strings.Join(ors, " OR ") + ")"
}

// tierClause is the custom-driver TIER dimension over cbp_driver_tiers, toggled
// independently via tiersBooleanMap (per-tier) + includeUnknownTier (no tier).
// "" when every present tier is enabled and unknown-tier is included.
func (af *alterationFilter) tierClause() string {
	if af == nil || len(af.TiersBooleanMap) == 0 {
		return ""
	}
	var enabled, disabled []string
	for t, on := range af.TiersBooleanMap {
		if on {
			enabled = append(enabled, t)
		} else {
			disabled = append(disabled, t)
		}
	}
	// Fast path: all tiers enabled + unknown included → no filter.
	if len(disabled) == 0 && inc(af.IncludeUnknownTier) {
		return ""
	}
	var ors []string
	if len(enabled) > 0 {
		ors = append(ors, "cbp_driver_tiers IN ("+inList(enabled)+")")
	}
	if inc(af.IncludeUnknownTier) {
		ors = append(ors, "cbp_driver_tiers IN ('', 'NA')")
	}
	if len(ors) == 0 {
		return falseClause
	}
	return "(" + strings.Join(ors, " OR ") + ")"
}

// combine ANDs status (over statusCol) + binary oncogenicity + tier predicates.
// "" for no filter, falseClause if any dimension excludes everything.
func (af *alterationFilter) combine(statusCol string) string {
	if af == nil {
		return ""
	}
	var clauses []string
	for _, c := range []string{af.statusClause(statusCol), af.oncogenicityClause(), af.tierClause()} {
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

// cnaFilterClause filters CNA gene counts by oncogenicity + tier (CNA has no
// somatic/germline status).
func (af *alterationFilter) cnaFilterClause() string {
	if af == nil {
		return ""
	}
	var clauses []string
	for _, c := range []string{af.oncogenicityClause(), af.tierClause()} {
		if c == falseClause {
			return falseClause
		}
		if c != "" {
			clauses = append(clauses, c)
		}
	}
	return strings.Join(clauses, " AND ")
}

// andClause joins a base scope with an optional extra predicate.
func andClause(base, extra string) string {
	if extra == "" {
		return base
	}
	return base + " AND " + extra
}
