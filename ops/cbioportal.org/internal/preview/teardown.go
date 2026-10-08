package preview

import (
	"context"
	"errors"
	"fmt"
	"strings"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
	"github.com/cbioportal/cbioportal-go/ops/cbioportal.org/internal/github"
	"github.com/cbioportal/cbioportal-go/ops/cbioportal.org/internal/job"
)

// Teardown drops the databases of pull request n's preview and marks its
// Deployments inactive.
func Teardown(ctx context.Context, st *Settings, gh *github.Client, n int) error {
	c := ch.New(job.ClickHouseURL(), "default")
	var errs []error
	for _, d := range DatabasesFor(st.Prefix, n).All() {
		if _, err := c.ExecuteServerwide(fmt.Sprintf("DROP DATABASE IF EXISTS `%s` SYNC", d)); err != nil {
			errs = append(errs, err)
			continue
		}
		job.Logf("dropped %s", d)
	}
	if err := gh.DeactivateEnvironment(ctx, st.GoRepo, fmt.Sprintf("pr-%d-preview", n)); err != nil {
		job.Logf("WARN: could not mark the deployments of #%d inactive: %v", n, err)
	}
	return errors.Join(errs...)
}

// GC tears down every preview whose pull request is closed or no longer
// has the preview label. A pull request that cannot be read is left alone.
func GC(ctx context.Context, st *Settings, gh *github.Client) error {
	c := ch.New(job.ClickHouseURL(), "default")
	names, err := job.Query(c, fmt.Sprintf("SELECT name FROM system.databases WHERE startsWith(name, '%s') FORMAT TSV", ch.SQLLit(st.Prefix)))
	if err != nil {
		return err
	}
	var errs []error
	for _, n := range PRNumbers(st.Prefix, strings.Split(names, "\n")) {
		pr, err := gh.PR(ctx, st.GoRepo, n)
		state, labelled := "", false
		if err == nil {
			state, labelled = pr.State, pr.HasLabel(st.Label)
		}
		d := DecideGC(state, labelled, err, st.Label)
		job.Logf("#%d: %s", n, d.Reason)
		if d.Remove {
			if err := Teardown(ctx, st, gh, n); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
