// Command e2ereport summarizes a Playwright run from its JSON report.
//
// The e2e config writes test-results/report.json (reporter: json) on every run;
// parsing that is reliable, unlike scraping the line reporter's stdout (which
// does not flush when backgrounded, yielding 0-byte logs).
//
//	go run ./cmd/e2ereport [report.json]   # default: the e2e test-results/report.json
//	go run ./cmd/e2ereport --by-spec       # failed counts grouped by spec file
//	go run ./cmd/e2ereport --fails         # list failing tests only
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

type suite struct {
	File   string  `json:"file"`
	Specs  []spec  `json:"specs"`
	Suites []suite `json:"suites"`
}
type spec struct {
	Title string `json:"title"`
	Tests []struct {
		Status string `json:"status"` // playwright "expected" | "unexpected" | "skipped" | "flaky"
	} `json:"tests"`
}
type result struct{ file, title, status string }

func walk(s suite, file string, out *[]result) {
	if s.File != "" {
		file = s.File
	}
	for _, sp := range s.Specs {
		status := "passed"
		allSkip := true
		for _, tst := range sp.Tests {
			if tst.Status != "skipped" {
				allSkip = false
			}
			if tst.Status != "expected" && tst.Status != "skipped" {
				status = "failed"
			}
		}
		if allSkip {
			status = "skipped"
		}
		*out = append(*out, result{file, sp.Title, status})
	}
	for _, c := range s.Suites {
		walk(c, file, out)
	}
}

func main() {
	byspec, fails := false, false
	path := ""
	for _, a := range os.Args[1:] {
		switch a {
		case "--by-spec":
			byspec = true
		case "--fails":
			fails = true
		default:
			path = a
		}
	}
	if path == "" {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, "projects/cbioportal-rust/cbioportal-frontend/end-to-end-test-playwright/test-results/report.json")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Printf("no report at %s (run the suite without --reporter so the config's json reporter writes it)\n", path)
		os.Exit(2)
	}
	var root struct {
		Suites []suite `json:"suites"`
	}
	if err := json.Unmarshal(b, &root); err != nil {
		fmt.Printf("parse %s: %v\n", path, err)
		os.Exit(2)
	}
	var res []result
	for _, s := range root.Suites {
		walk(s, "", &res)
	}
	var p, f, sk int
	perSpec := map[string]int{}
	for _, r := range res {
		switch r.status {
		case "passed":
			p++
		case "failed":
			f++
			perSpec[filepath.Base(r.file)]++
			if fails || !byspec {
				fmt.Printf("  FAIL  %s :: %.70s\n", filepath.Base(r.file), r.title)
			}
		case "skipped":
			sk++
		}
	}
	if byspec {
		type kv struct {
			spec string
			n    int
		}
		var list []kv
		for s, n := range perSpec {
			list = append(list, kv{s, n})
		}
		sort.Slice(list, func(i, j int) bool { return list[i].n > list[j].n })
		for _, e := range list {
			fmt.Printf("  %3d  %s\n", e.n, e.spec)
		}
	}
	fmt.Printf("\n%d passed / %d failed / %d skipped  (%d specs)\n", p, f, sk, len(res))
}
