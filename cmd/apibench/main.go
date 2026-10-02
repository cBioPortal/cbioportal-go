// Command apibench times representative heavy API requests (study view,
// results view, enrichments) against a running cbioportal-api, to find and
// track slow endpoints.
//
//	go run ./cmd/apibench -api http://localhost:18091 -study msk_impact_2017 -n 3
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"
	"time"
)

type request struct {
	name, method, path, body string
}

func requests(study string) []request {
	svf := fmt.Sprintf(`{"studyIds":["%s"]}`, study)
	filtered := fmt.Sprintf(`{"studyIds":["%s"],"clinicalDataFilters":[{"attributeId":"SAMPLE_TYPE","values":[{"value":"Primary"}]}]}`, study)
	return []request{
		{"filtered-samples", "POST", "/api/filtered-samples/fetch", filtered},
		{"clinical-data-counts", "POST", "/api/clinical-data-counts/fetch",
			fmt.Sprintf(`{"attributes":[{"attributeId":"CANCER_TYPE"},{"attributeId":"SAMPLE_TYPE"},{"attributeId":"SEX"}],"studyViewFilter":%s}`, filtered)},
		{"clinical-data-bin-counts", "POST", "/api/clinical-data-bin-counts/fetch?dataBinMethod=STATIC",
			fmt.Sprintf(`{"attributes":[{"attributeId":"MUTATION_COUNT"},{"attributeId":"FRACTION_GENOME_ALTERED"}],"studyViewFilter":%s}`, filtered)},
		{"mutated-genes", "POST", "/api/mutated-genes/fetch", svf},
		{"mutated-genes-filtered", "POST", "/api/mutated-genes/fetch", filtered},
		{"cna-genes", "POST", "/api/cna-genes/fetch", svf},
		{"sv-genes", "POST", "/api/structuralvariant-genes/fetch", svf},
		{"molecular-profile-sample-counts", "POST", "/api/molecular-profile-sample-counts/fetch", svf},
		{"sample-lists-counts", "POST", "/api/sample-lists-counts/fetch", svf},
		{"mutation-data-counts", "POST", "/api/mutation-data-counts/fetch",
			fmt.Sprintf(`{"genomicDataFilters":[{"hugoGeneSymbol":"TP53","profileType":"mutations"}],"studyViewFilter":%s}`, svf)},
		{"samples-fetch", "POST", "/api/samples/fetch?projection=DETAILED", fmt.Sprintf(`{"sampleListIds":["%s_all"]}`, study)},
		{"mutations-fetch", "POST", fmt.Sprintf("/api/molecular-profiles/%s_mutations/mutations/fetch?projection=DETAILED", study),
			fmt.Sprintf(`{"sampleListId":"%s_all","entrezGeneIds":[7157,1956,3845]}`, study)},
		{"gene-panel-data", "POST", "/api/gene-panel-data/fetch",
			fmt.Sprintf(`{"molecularProfileIds":["%s_mutations"]}`, study)},
		{"studies", "GET", "/api/studies?projection=DETAILED", ""},
		{"clinical-attributes", "POST", "/api/clinical-attributes/fetch", fmt.Sprintf(`["%s"]`, study)},
	}
}

func main() {
	api := flag.String("api", "http://localhost:18091", "API base URL")
	study := flag.String("study", "msk_impact_2017", "study to query")
	n := flag.Int("n", 3, "repetitions per request (median reported)")
	flag.Parse()

	client := &http.Client{Timeout: 5 * time.Minute}
	type result struct {
		name   string
		median time.Duration
		bytes  int
		err    string
	}
	var results []result
	for _, rq := range requests(*study) {
		var times []time.Duration
		res := result{name: rq.name}
		for i := 0; i < *n; i++ {
			req, _ := http.NewRequest(rq.method, *api+rq.path, strings.NewReader(rq.body))
			req.Header.Set("Content-Type", "application/json")
			start := time.Now()
			resp, err := client.Do(req)
			if err != nil {
				res.err = err.Error()
				break
			}
			var buf bytes.Buffer
			io.Copy(&buf, resp.Body)
			resp.Body.Close()
			times = append(times, time.Since(start))
			res.bytes = buf.Len()
			if resp.StatusCode != 200 {
				res.err = fmt.Sprintf("HTTP %d: %.120s", resp.StatusCode, buf.String())
			}
		}
		if len(times) > 0 {
			slices.Sort(times)
			res.median = times[len(times)/2]
		}
		results = append(results, res)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].median > results[j].median })
	for _, r := range results {
		fmt.Fprintf(os.Stdout, "%-34s %8.0f ms %10d B %s\n", r.name, float64(r.median.Microseconds())/1000, r.bytes, r.err)
	}
}
