package core

// AlterationFilter is the study-view AlterationFilter: which mutation/SV
// statuses (somatic/germline/unknown) and oncogenicities (driver/VUS/unknown,
// incl. custom tiers) to count. Bool pointers distinguish "not sent" (nil →
// include) from an explicit false.
type AlterationFilter struct {
	IncludeDriver                  *bool           `json:"includeDriver"`
	IncludeVUS                     *bool           `json:"includeVUS"`
	IncludeUnknownOncogenicity     *bool           `json:"includeUnknownOncogenicity"`
	IncludeGermline                *bool           `json:"includeGermline"`
	IncludeSomatic                 *bool           `json:"includeSomatic"`
	IncludeUnknownStatus           *bool           `json:"includeUnknownStatus"`
	IncludeUnknownTier             *bool           `json:"includeUnknownTier"`
	TiersBooleanMap                map[string]bool `json:"tiersBooleanMap"`
	MutationEventTypes             map[string]bool `json:"mutationEventTypes"`
	CopyNumberAlterationEventTypes map[string]bool `json:"copyNumberAlterationEventTypes"`
	StructuralVariants             *bool           `json:"structuralVariants"`
}

// FalseClause is a SQL predicate no row satisfies.
const FalseClause = "1 = 0"

// IncludeFlag treats a nil (unsent) flag as "include".
func IncludeFlag(b *bool) bool { return b == nil || *b }
