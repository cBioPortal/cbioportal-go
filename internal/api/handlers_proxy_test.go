package api

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
)

func TestOncokbProxyStreamsEncodedResponse(t *testing.T) {
	big := strings.Repeat(`{"query":{"hugoSymbol":"BRAF"},"oncogenic":"Oncogenic"},`, 50000)
	var gotPath, gotQuery, gotBody, gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotPath, gotQuery, gotBody, gotAuth = r.URL.Path, r.URL.RawQuery, string(b), r.Header.Get("Authorization")
		if strings.HasSuffix(r.URL.Path, "/missing") {
			http.Error(w, "nope", http.StatusNotFound)
			return
		}
		w.Write([]byte(big))
	}))
	defer upstream.Close()
	t.Setenv("ONCOKB_URL", upstream.URL+"/api/v1")
	t.Setenv("ONCOKB_TOKEN", "tok")
	h := NewRouter(newState(&core.State{}))

	enc := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	body := `[{"gene":{"hugoGeneSymbol":"BRAF"},"proteinChange":"V600E"}]`
	req := httptest.NewRequest(http.MethodPost, oncokbProxyPrefix+enc("/annotate/mutations/byProteinChange")+
		"?"+enc("referenceGenome")+"="+enc("GRCh37"), strings.NewReader(`"`+enc(body)+`"`))
	req.Header.Set("X-Proxy-User-Agreement", oncokbUserAgreement)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != `"`+enc(big)+`"` {
		t.Fatalf("status %d, body %d bytes", rec.Code, rec.Body.Len())
	}
	if gotPath != "/api/v1/annotate/mutations/byProteinChange" || gotQuery != "referenceGenome=GRCh37" ||
		gotBody != body || gotAuth != "Bearer tok" {
		t.Errorf("upstream got %q %q %q %q", gotPath, gotQuery, gotBody, gotAuth)
	}

	req = httptest.NewRequest(http.MethodGet, oncokbProxyPrefix+enc("/missing"), nil)
	req.Header.Set("X-Proxy-User-Agreement", oncokbUserAgreement)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound || rec.Body.String() != "nope\n\n" {
		t.Errorf("error passthrough: %d %q", rec.Code, rec.Body.String())
	}
}

func TestOncokbProxyNoTimeoutButCancels(t *testing.T) {
	if oncokbClient.Timeout != 0 {
		t.Fatalf("oncokbClient.Timeout = %v", oncokbClient.Timeout)
	}
	cancelled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		close(cancelled)
	}))
	defer upstream.Close()
	t.Setenv("ONCOKB_URL", upstream.URL)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/proxy/dev/oncokb/utils/allCuratedGenes", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		NewRouter(newState(&core.State{})).ServeHTTP(httptest.NewRecorder(), req)
		close(done)
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream request not cancelled")
	}
	<-done
}
