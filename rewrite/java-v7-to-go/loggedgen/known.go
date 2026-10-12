package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
)

// expectation is the comparison setting of logged requests whose difference
// from production is expected: options appended to the corpus header (an
// ignore list, known="reason", known-if-empty="reason",
// known-if-b-fails="reason", known-if-a-subset="reason" or
// known-if-b-subset="reason") and, for ignored
// keys, a comment with the reason.
type expectation struct {
	match   func(method, path string, query url.Values, body string) bool
	options string
	comment string
}

func pathIs(re string) func(string) bool {
	r := regexp.MustCompile(re)
	return r.MatchString
}

var (
	isSession         = pathIs(`^/api/session(/|$)`)
	isCustomCounts    = pathIs(`^/api/custom-data-counts/fetch$`)
	isInfo            = pathIs(`^/api/info$`)
	isAPIDocs         = pathIs(`^/api/(v2|v3)/api-docs(/|$)`)
	isStudyList       = pathIs(`^/api/studies$`)
	isStudy           = pathIs(`^/api/studies/[^/]+$`)
	isLegacyStudies   = pathIs(`^/api/legacy/studies(/[^/]+)?$`)
	isStudyClinical   = pathIs(`^/api/studies/[^/]+/clinical-data$`)
	isStudiesFetch    = pathIs(`^/api/studies/fetch$`)
	isSampleList      = pathIs(`^/api/samples$`)
	isClinicalTable   = pathIs(`^/api/clinical-data-table/fetch$`)
	isProfileSamples  = pathIs(`^/api/molecular-profile-sample-counts/fetch$`)
	isCaseListCounts  = pathIs(`^/api/sample-lists-counts/fetch$`)
	isListMutations   = pathIs(`^/api/molecular-profiles/[^/]+/mutations(/fetch)?$`)
	isEntityList      = pathIs(`^/api/(studies/[^/]+/)?(samples|patients)$`)
	isTreatmentRows   = pathIs(`^/api/treatments/(patient|sample)$`)
	isClinicalFetch   = pathIs(`^/api/clinical-data/fetch$`)
	isEventPatients   = pathIs(`^/api/(clinical-events-meta|survival-data)/fetch$`)
	isSVCounts        = pathIs(`^/api/structuralvariant-counts/fetch$`)
	isGisticRegions   = pathIs(`^/api/studies/[^/]+/significant-copy-number-regions$`)
	isDiscreteCNAList = pathIs(`^/api/molecular-profiles/[^/]+/discrete-copy-number(/fetch)?$`)
	isMolecularData   = pathIs(`^/api/(molecular-profiles/[^/]+/molecular-data(/fetch)?|molecular-data/fetch)$`)
	isKeywordSorted   = pathIs(`^/api/(legacy/studies|genes|(studies/[^/]+/)?patients)/?$`)
	isMutationsFetch  = pathIs(`^/api/(legacy/)?mutations/fetch$`)
	isDataBinCounts   = pathIs(`^/api/(legacy/)?(genomic|generic-assay)-data-bin-counts/fetch$`)
)

// paged reports a request for one page of a list.
func paged(q url.Values) bool { return q.Has("pageSize") || q.Has("pageNumber") }

// addedPaging matches the endpoints go pages and sorts although their Java
// controllers declare no paging parameters (core.AddedPagedEndpoints).
var addedPaging = func() []func(method, path string) bool {
	var out []func(method, path string) bool
	for _, a := range core.AddedPagedEndpoints {
		re := regexp.MustCompile(`^` + regexp.MustCompile(`\\\{[^/]*\}`).ReplaceAllString(regexp.QuoteMeta(a.Path), `[^/]+`) + `$`)
		method := a.Method
		out = append(out, func(m, p string) bool { return m == method && re.MatchString(p) })
	}
	return out
}()

// usesAddedPaging reports a request that pages or sorts an endpoint whose
// paging go adds.
func usesAddedPaging(method, path string, q url.Values) bool {
	if !paged(q) && !q.Has("sortBy") && !q.Has("direction") {
		return false
	}
	for _, m := range addedPaging {
		if m(method, path) {
			return true
		}
	}
	return false
}

// bodyObject decodes a JSON object body, or nil.
func bodyObject(body string) map[string]json.RawMessage {
	var f map[string]json.RawMessage
	if json.Unmarshal([]byte(body), &f) != nil {
		return nil
	}
	return f
}

// lacksIdentifiers reports a clinical-data/fetch filter without identifiers.
func lacksIdentifiers(body string) bool {
	f := bodyObject(body)
	if f == nil {
		return false
	}
	ids, ok := f["identifiers"]
	return !ok || string(ids) == "null"
}

// patientCount is the number of distinct patient ids in a body's
// patientIdentifiers.
func patientCount(body string) int {
	var f struct {
		PatientIdentifiers []struct {
			PatientID string `json:"patientId"`
		} `json:"patientIdentifiers"`
	}
	if json.Unmarshal([]byte(body), &f) != nil {
		return 0
	}
	seen := map[string]bool{}
	for _, p := range f.PatientIdentifiers {
		seen[p.PatientID] = true
	}
	return len(seen)
}

// hasGenes reports a request that names genes, in its query or its body.
func hasGenes(q url.Values, body string) bool {
	if q.Has("entrezGeneId") || q.Has("entrezGeneIds") {
		return true
	}
	f := bodyObject(body)
	ids, ok := f["entrezGeneIds"]
	return ok && string(ids) != "null"
}

// skip reports a request too costly to replay against production every
// night, gigabytes from Java even when go is asked for one page of it:
//   - the whole copy-number matrix of a profile (event type ALL keeps the
//     diploid values too) for every gene;
//   - one page of a profile's values for every gene. Java's molecular-data
//     controllers have no paging parameters and answer the whole matrix,
//     so the answers differ anyway (go pages them, a known difference);
//   - a molecular-data fetch that names no genes, which asks for whole
//     matrices. Java's filters do not require entrezGeneIds. Where the body
//     also has a field the filter lacks (such as sampleIds or genes on
//     molecular-data/fetch), Java answers an empty 200 and go the matrices.
func skip(method, target, body string) bool {
	path, rawQuery, _ := strings.Cut(target, "?")
	path = strings.TrimSuffix(path, "/")
	q, _ := url.ParseQuery(rawQuery)
	switch {
	case isDiscreteCNAList(path):
		return strings.EqualFold(q.Get("discreteCopyNumberEventType"), "ALL") && !hasGenes(q, body)
	case isMolecularData(path):
		return (paged(q) || method == "POST") && !hasGenes(q, body)
	}
	return false
}

// versionFields are /api/info's fields that describe the deployment.
const versionFields = "gitBranch,gitCommitId,gitCommitIdAbbrev,gitCommitIdDescribe,gitCommitIdDescribeShort," +
	"gitCommitMessageFull,gitCommitMessageShort,gitCommitMessageUserEmail,gitCommitMessageUserName," +
	"portalVersion,gitDirty,dbVersion,derivedTableVersion"

var expectations = []expectation{
	{
		match:   func(m, p string, q url.Values, b string) bool { return isSession(p) },
		options: `known="www's session store: its sessions and generated ids exist only there"`,
	},
	{
		match: func(m, p string, q url.Values, b string) bool {
			return isCustomCounts(p) || strings.Contains(b, `"customDataFilters"`)
		},
		options: `known="custom data ids are sessions in www's session store"`,
	},
	{
		match:   func(m, p string, q url.Values, b string) bool { return isInfo(p) },
		options: "ignore=" + versionFields,
		comment: "version fields describe the deployment",
	},
	{
		match: func(m, p string, q url.Values, b string) bool { return isAPIDocs(p) },
		options: `ignore=host,servers,oauth2RedirectUrl known="go documents the paging and sorting parameters it adds ` +
			`(Added in go; docs/rewrite/api-coverage.md#paging-that-go-adds)"`,
		comment: "the API documentation names the origin it is served from",
	},
	{
		match:   func(m, p string, q url.Values, b string) bool { return m == "GET" && isStudyList(p) },
		options: `known="www lists resource definitions of other studies sharing a resource id (CancerStudyMapper joins resource_definition on resource_id only)"`,
	},
	{
		match:   func(m, p string, q url.Values, b string) bool { return m == "GET" && isStudy(p) },
		options: "ignore=resourceCounts",
		comment: "www counts resource definitions of other studies sharing a resource id (CancerStudyMapper joins resource_definition on resource_id only)",
	},
	{
		match: func(m, p string, q url.Values, b string) bool {
			return m == "GET" && isKeywordSorted(p) && q.Get("keyword") != "" && q.Get("sortBy") != ""
		},
		options: `known="Java ignores sortBy and direction in a keyword search (StudyMapper, GeneMapper, PatientMapper); go sorts the matches as asked"`,
	},
	{
		match: func(m, p string, q url.Values, b string) bool {
			return m == "GET" && isLegacyStudies(p) && paged(q) && !q.Has("sortBy") && !q.Has("keyword") &&
				!strings.EqualFold(q.Get("projection"), "ID")
		},
		options: `known="without sortBy or keyword Java's legacy StudyMapper.getStudies has no ORDER BY, so which studies fill a page is unspecified"`,
	},
	{
		match: func(m, p string, q url.Values, b string) bool {
			return (m == "POST" && isStudiesFetch(p)) || (m == "GET" && isLegacyStudies(p))
		},
		options: "ignore=allSampleCount,resourceCounts,treatmentCount",
		comment: "Java's legacy StudyMapper reports allSampleCount 1, joins resource_definition on resource_id only, listing other studies' definitions of a resource, " +
			"and counts only events of type 'Treatment', so studies whose events are 'TREATMENT' report 0 treated patients",
	},
	{
		match: func(m, p string, q url.Values, b string) bool {
			return m == "GET" && isSampleList(p) && q.Has("keyword") && q.Has("pageSize")
		},
		options: `known="a sample id in several studies ties in the keyword order, which Java leaves unspecified, so the page boundary picks either"`,
	},
	{
		match: func(m, p string, q url.Values, b string) bool {
			return isClinicalTable(p) && q.Get("sortBy") != "" && q.Has("pageSize")
		},
		options: `known="Java orders by the sort value only; which of the tied samples fill the page is unspecified"`,
	},
	{
		match: func(m, p string, q url.Values, b string) bool {
			return isClinicalTable(p) && q.Get("sortBy") == "" && q.Has("pageSize")
		},
		options: `known="without sortBy Java's getVisibleSampleInternalIdsForClinicalTable has no ORDER BY, so which samples fill a page is unspecified (production answers a different page on repeated calls)"`,
	},
	{
		match: func(m, p string, q url.Values, b string) bool {
			return m == "GET" && isStudyClinical(p) && q.Has("pageSize")
		},
		options: `known="Java orders by the sample's or patient's internal id only; which of the attributes of the entity at a page boundary fall on the page follows ClickHouse's unstable sort"`,
	},
	{
		match: func(m, p string, q url.Values, b string) bool {
			return isListMutations(p) && paged(q) && q.Get("sortBy") == ""
		},
		options: `known="Java orders a profile's mutations by profile, sample and gene only (MutationMapper); which of a sample's mutations in one gene fall on a page at its boundary follows ClickHouse's unstable sort"`,
	},
	{
		match: func(m, p string, q url.Values, b string) bool {
			return m == "GET" && isEntityList(p) && paged(q) && q.Get("sortBy") == "" && !q.Has("keyword") &&
				!strings.EqualFold(q.Get("projection"), "ID")
		},
		options: `known="without sortBy or keyword Java's SUMMARY and DETAILED sample and patient lists have no ORDER BY (ClickhouseSampleMapper, PatientMapper), so which entities fill a page is unspecified (production answers a different page on repeated calls)"`,
	},
	{
		match: func(m, p string, q url.Values, b string) bool {
			return m == "GET" && isGisticRegions(p) && paged(q) && q.Get("sortBy") == ""
		},
		options: `known="without sortBy go lists GISTIC regions by amp, chromosome and position, not Java's import order (gistic_roi_id); unsorted order is not copied, so a page holds other regions"`,
	},
	{
		match:   func(m, p string, q url.Values, b string) bool { return usesAddedPaging(m, p, q) },
		options: `known="go pages and sorts this endpoint (docs/rewrite/api-coverage.md#paging-that-go-adds); Java's controller has no such parameters and answers the whole list, or fails on it"`,
	},
	{
		match:   func(m, p string, q url.Values, b string) bool { return m == "POST" && isTreatmentRows(p) },
		options: `known-if-empty="for studies or samples without treatment data Java's TreatmentServiceImpl filters every sample out, TreatmentMapper then builds no WHERE clause for the empty lists, and production lists the treatments of the whole portal"`,
	},
	{
		match: func(m, p string, q url.Values, b string) bool {
			return m == "POST" && isClinicalFetch(p) && lacksIdentifiers(b)
		},
		options: `known="without identifiers Java's ClinicalDataController.fetchClinicalData reads the identifiers of a filter that has none and fails with a 500; go answers an empty list"`,
	},
	{
		match: func(m, p string, q url.Values, b string) bool {
			return m == "POST" && isEventPatients(p) && patientCount(b) > 1
		},
		options: `known="for more than one patient Java's ClinicalEventMapper calls SqlUtils.combineStudyAndPatientIds, which does not exist, and fails with a 500; go answers the data"`,
	},
	{
		match:   func(m, p string, q url.Values, b string) bool { return isSVCounts(p) },
		options: "ignore=matchingGenePanelIds,numberOfProfiledCases",
		comment: "Java's ProfiledCasesCounter adds a pair's gene2 panels to gene1's panel list, which later pairs share, so they report extra panels and profiled cases",
	},
	{
		match:   func(m, p string, q url.Values, b string) bool { return isProfileSamples(p) },
		options: "ignore=label",
		comment: "a profile type's label is the name of the profile ClickHouse's unordered GROUP BY yields first, in hash-table order that shifts with the filtered samples",
	},
	{
		match: func(m, p string, q url.Values, b string) bool {
			return m == "POST" && isMutationsFetch(p) && strings.Contains(b, `"sampleMolecularIdentifiers"`)
		},
		options: `known-if-a-subset="for (profile, sample) pairs Java's MutationMapper selects the samples, not the profiles, so it adds the samples' records of the study's other mutation profiles (such as _uncalled); go answers the named profiles' records (Fixed in go; docs/rewrite/java-quirks.md)"`,
	},
	{
		match:   func(m, p string, q url.Values, b string) bool { return m == "POST" && isDataBinCounts(p) },
		options: `known-if-b-subset="Java's StudyViewMapper adds one NA bin for the whole request, attached to whichever gene or entity ClickHouse lists first, so the others have none; go gives each its own NA bin (Fixed in go; docs/rewrite/java-quirks.md)"`,
	},
	{
		match:   func(m, p string, q url.Values, b string) bool { return isCaseListCounts(p) },
		options: "ignore=label",
		comment: "a case list type's label is the name of the list ClickHouse's unordered GROUP BY yields first (mergeCaseListCounts), in hash-table order that shifts with the filtered samples",
	},
}

// slashedIntercepted is the expectation of a POST with a trailing slash to
// a legacy endpoint whose filter Java's InvolvedCancerStudyExtractorInterceptor
// parses: that interceptor matches the exact path, so Java's controller
// fails on the request, while go answers it as without the slash.
const slashedIntercepted = `known-if-b-fails="with a trailing slash Java's InvolvedCancerStudyExtractorInterceptor, which matches exact paths, leaves the filter unparsed and the controller fails; go answers as without the slash (Fixed in go; docs/rewrite/java-quirks.md)"`

// unknownIntercepted is the expectation of every POST to a legacy endpoint
// whose filter Java's InvolvedCancerStudyExtractorInterceptor parses: it
// fails on a property the filter does not declare and Java answers an
// empty 200, while go answers as without the property.
const unknownIntercepted = `known-if-b-empty="Java's InvolvedCancerStudyExtractorInterceptor fails on a filter property it does not declare and answers an empty 200; go answers as without the property (Fixed in go; docs/rewrite/java-quirks.md)"`

// expect returns the extra header options and comment for a request. Java
// matches a path with a trailing slash as the path without it, except for
// the filter of a legacy multi-study POST (slashedIntercepted).
func expect(method, target, body string) (options, comment string) {
	path, rawQuery, _ := strings.Cut(target, "?")
	slashed := strings.HasSuffix(path, "/")
	path = strings.TrimSuffix(path, "/")
	intercepted := method == "POST" && core.InterceptedBodyPath(path)
	if intercepted && slashed {
		return slashedIntercepted, ""
	}
	q, _ := url.ParseQuery(rawQuery)
	for _, e := range expectations {
		if e.match(method, path, q, body) {
			if intercepted {
				return strings.TrimSpace(e.options + " " + unknownIntercepted), e.comment
			}
			return e.options, e.comment
		}
	}
	if intercepted {
		return unknownIntercepted, ""
	}
	return "", ""
}

// expectationOptions are the header options expect sets; annotate replaces
// them.
var expectationOptions = regexp.MustCompile(` (known|known-if-empty|known-if-b-fails|known-if-b-empty|known-if-a-subset|known-if-b-subset)="[^"]*"| ignore=\S+`)

// annotate copies a corpus written by this command with the expectations
// marked as they stand now: the options and comments of earlier
// expectations are replaced, and requests that are now skipped are left
// out (their number goes to report).
func annotate(r io.Reader, w io.Writer, report io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	var lines []string
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return err
	}
	bw := bufio.NewWriter(w)
	var comments []string
	skipped := 0
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		switch {
		case strings.HasPrefix(line, "# "):
			// A comment right before an entry is an expectation's.
			comments = append(comments, line)
			continue
		case !strings.HasPrefix(line, "## ") || i+1 >= len(lines):
			for _, c := range comments {
				fmt.Fprintln(bw, c)
			}
			comments = nil
			fmt.Fprintln(bw, line)
			continue
		}
		comments = nil
		header := expectationOptions.ReplaceAllString(line, "")
		method, target, _ := strings.Cut(lines[i+1], " ")
		body := ""
		if i+2 < len(lines) && !strings.HasPrefix(lines[i+2], "#") {
			body = lines[i+2]
		}
		if skip(method, target, body) {
			skipped++
			for i+1 < len(lines) && !strings.HasPrefix(lines[i+1], "## ") && !strings.HasPrefix(lines[i+1], "# ") {
				i++
			}
			continue
		}
		if opts, comment := expect(method, target, body); opts != "" {
			if comment != "" {
				fmt.Fprintln(bw, "# "+comment)
			}
			header += " " + opts
		}
		fmt.Fprintln(bw, header)
	}
	for _, c := range comments {
		fmt.Fprintln(bw, c)
	}
	if skipped > 0 && report != nil {
		fmt.Fprintf(report, "loggedgen: left out %d requests for whole profile matrices\n", skipped)
	}
	return bw.Flush()
}
