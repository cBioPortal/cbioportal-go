package studies

import (
	"testing"

	"github.com/cbioportal/cbioportal-go/internal/api/core"
)

// TestSortables pins that the endpoints of this package paging in SQL can
// sort by every sortBy value they accept.
func TestSortables(t *testing.T) {
	for _, c := range []struct {
		sort     core.Param
		sortable map[string]string
	}{
		{core.ClinicalDataSort, clinicalDataSortable},
		{core.SampleSort, sampleSortable},
		{core.PatientSort, patientSortable},
	} {
		for _, v := range c.sort.Values {
			if _, ok := c.sortable[v]; !ok {
				t.Errorf("%s: no SQL for %s", c.sort.Class, v)
			}
		}
		if len(c.sortable) != len(c.sort.Values) {
			t.Errorf("%s: %d SQL sorts for %d values", c.sort.Class, len(c.sortable), len(c.sort.Values))
		}
	}
}
