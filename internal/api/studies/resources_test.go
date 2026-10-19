package studies

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
)

// TestResourceDefinitionPriorityNumeric pins that a priority-sorted page of
// resource definitions keeps SQL's numeric order: the handler claims it, so
// PageList does not re-sort the JSON strings ("10" < "2").
func TestResourceDefinitionPriorityNumeric(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if clause := core.SQLPageOrder(r, resourceDefinitionSort, "resource_id ASC"); !strings.Contains(clause, "priority ASC") {
			t.Errorf("clause %q", clause)
		}
		two, ten := "2", "10"
		core.WriteJSON(w, []ResourceDefinition{{ResourceID: "a", Priority: &two}, {ResourceID: "b", Priority: &ten}})
	})
	got := httptest.NewRecorder()
	core.PageList(handler).ServeHTTP(got, httptest.NewRequest("GET", "/api/studies/s/resource-definitions?sortBy=priority", nil))
	var defs []ResourceDefinition
	json.Unmarshal(got.Body.Bytes(), &defs)
	if len(defs) != 2 || defs[0].ResourceID != "a" {
		t.Errorf("got %s", got.Body.String())
	}
}
