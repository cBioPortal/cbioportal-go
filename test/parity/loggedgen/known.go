package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
)

// expectation is the comparison setting of logged requests whose difference
// from production is expected: options appended to the corpus header (an
// ignore list, or known="reason") and, for ignored keys, a comment with the
// reason.
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
	isSession        = pathIs(`^/api/session(/|$)`)
	isCustomCounts   = pathIs(`^/api/custom-data-counts/fetch$`)
	isInfo           = pathIs(`^/api/info$`)
	isAPIDocs        = pathIs(`^/api/(v2|v3)/api-docs(/|$)`)
	isStudyList      = pathIs(`^/api/studies/?$`)
	isStudy          = pathIs(`^/api/studies/[^/]+/?$`)
	isStudyClinical  = pathIs(`^/api/studies/[^/]+/clinical-data/?$`)
	isStudiesFetch   = pathIs(`^/api/studies/fetch$`)
	isSampleList     = pathIs(`^/api/samples/?$`)
	isClinicalTable  = pathIs(`^/api/clinical-data-table/fetch$`)
	isProfileSamples = pathIs(`^/api/molecular-profile-sample-counts/fetch$`)
	isCaseListCounts = pathIs(`^/api/sample-lists-counts/fetch$`)
	isListMutations  = pathIs(`^/api/molecular-profiles/[^/]+/mutations(/fetch)?/?$`)
	isEntityList     = pathIs(`^/api/(studies/[^/]+/)?(samples|patients)/?$`)
	isTreatmentRows  = pathIs(`^/api/treatments/(patient|sample)/?$`)
)

// paged reports a request for one page of a list.
func paged(q url.Values) bool { return q.Has("pageSize") || q.Has("pageNumber") }

// onlyStudyIDs reports a StudyViewFilter body that names studies and nothing
// else.
func onlyStudyIDs(body string) bool {
	var f map[string]json.RawMessage
	if json.Unmarshal([]byte(body), &f) != nil {
		return false
	}
	_, ok := f["studyIds"]
	return ok && len(f) == 1
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
		match:   func(m, p string, q url.Values, b string) bool { return isAPIDocs(p) },
		options: "ignore=host,servers,oauth2RedirectUrl",
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
		match:   func(m, p string, q url.Values, b string) bool { return m == "POST" && isStudiesFetch(p) },
		options: `known="www legacy studies/fetch reports allSampleCount 1"`,
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
			return m == "POST" && isTreatmentRows(p) && onlyStudyIDs(b)
		},
		options: `known="for studies without treatment data Java's TreatmentServiceImpl filters every sample out, TreatmentMapper then builds no WHERE clause for the empty lists, and production lists the treatments of the whole portal"`,
	},
	{
		match:   func(m, p string, q url.Values, b string) bool { return isProfileSamples(p) },
		options: "ignore=label",
		comment: "a profile type's label is the name of the profile ClickHouse's unordered GROUP BY yields first, in hash-table order that shifts with the filtered samples",
	},
	{
		match:   func(m, p string, q url.Values, b string) bool { return isCaseListCounts(p) },
		options: "ignore=label",
		comment: "a case list type's label is the name of the list ClickHouse's unordered GROUP BY yields first (mergeCaseListCounts), in hash-table order that shifts with the filtered samples",
	},
}

// expect returns the extra header options and comment for a request.
func expect(method, target, body string) (options, comment string) {
	path, rawQuery, _ := strings.Cut(target, "?")
	q, _ := url.ParseQuery(rawQuery)
	for _, e := range expectations {
		if e.match(method, path, q, body) {
			return e.options, e.comment
		}
	}
	return "", ""
}

// annotate copies a corpus written by this command, adding the expectations
// to its entries (headers that already carry them are left as they are).
func annotate(r io.Reader, w io.Writer) error {
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
	for i := 0; i < len(lines); i++ {
		header := lines[i]
		if !strings.HasPrefix(header, "## ") || i+1 >= len(lines) {
			fmt.Fprintln(bw, header)
			continue
		}
		method, target, _ := strings.Cut(lines[i+1], " ")
		body := ""
		if i+2 < len(lines) && !strings.HasPrefix(lines[i+2], "#") {
			body = lines[i+2]
		}
		if opts, comment := expect(method, target, body); opts != "" &&
			!strings.Contains(header, "known=") && !strings.Contains(header, "ignore=") {
			if comment != "" {
				fmt.Fprintln(bw, "# "+comment)
			}
			header += " " + opts
		}
		fmt.Fprintln(bw, header)
	}
	return bw.Flush()
}
