package main

import (
	"bufio"
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
	isStudiesFetch   = pathIs(`^/api/studies/fetch$`)
	isClinicalData   = pathIs(`^/api/studies/[^/]+/(clinical-data|(patients|samples)/[^/]+/clinical-data)$`)
	isSampleList     = pathIs(`^/api/samples/?$`)
	isClinicalTable  = pathIs(`^/api/clinical-data-table/fetch$`)
	isProfileSamples = pathIs(`^/api/molecular-profile-sample-counts/fetch$`)
)

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
		match:   func(m, p string, q url.Values, b string) bool { return m == "POST" && isStudiesFetch(p) },
		options: `known="www legacy studies/fetch reports allSampleCount 1"`,
	},
	{
		match: func(m, p string, q url.Values, b string) bool {
			return m == "GET" && isClinicalData(p) && q.Has("pageSize") && q.Get("sortBy") == ""
		},
		options: `known="Java pages clinical data in import (internal id) order, we in sample id order, so a page holds other samples"`,
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
		match:   func(m, p string, q url.Values, b string) bool { return isProfileSamples(p) },
		options: "ignore=label",
		comment: "a profile type's label is the name of the profile ClickHouse's GROUP BY yields first; it varies between www requests",
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
