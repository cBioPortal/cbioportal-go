package transform

import (
	"fmt"
	"strings"

	ch "github.com/cbioportal/cbioportal-go/internal/clickhouse"
)

// Filling a table's batches on several replicas at once (Tuning.Replicas)
// speeds up a full build because one replica writes the rows of an insert
// about as fast as it can: on ClickHouse Cloud, two batch inserts on two
// replicas take about 60% of the time of one after the other, with the
// memory of one insert on each replica.

// distinctReplicas makes pinReplicas give each lane a replica of its own.
// Tests turn it off to run lanes on one local server.
var distinctReplicas = true

// lane is a pinned client (ch.Client.Pinned) on one replica.
type lane struct {
	base *ch.Client
	cl   *ch.Client
	host string
}

// pinReplicas returns up to n lanes, each on a replica of its own: fewer
// when the service has fewer replicas, and none when n is 1 or the replica
// of a connection cannot be read. The load balancer of a ClickHouse Cloud
// service picks a replica for each new connection, so pinReplicas opens
// connections until each replica it wants has one.
func pinReplicas(base *ch.Client, n int) []*lane {
	if n < 2 {
		return nil
	}
	if have := replicaCount(base); have > 0 && distinctReplicas {
		n = min(n, have)
	}
	var lanes []*lane
	seen := map[string]bool{}
	for try := 0; try < 8*n && len(lanes) < n; try++ {
		cl := base.Pinned()
		host, err := cl.Replica()
		if err != nil || distinctReplicas && seen[host] {
			cl.Close()
			continue
		}
		seen[host] = true
		lanes = append(lanes, &lane{base: base, cl: cl, host: host})
	}
	return lanes
}

// replicaCount is how many replicas the service has, or 0 when it cannot
// be read: a server outside a cluster has no cluster named default.
func replicaCount(base *ch.Client) int {
	out, err := base.Execute("SELECT count() FROM clusterAllReplicas('default', system.one) FORMAT TabSeparated")
	if err != nil {
		return 0
	}
	var n int
	fmt.Sscan(strings.TrimSpace(out), &n)
	return n
}

// client returns the lane's pinned client once it is on the lane's
// replica: a connection the server closed is opened again, and the new
// one can reach another replica.
func (l *lane) client() (*ch.Client, error) {
	for try := 0; try < 20; try++ {
		host, err := l.cl.Replica()
		if err == nil && host == l.host {
			return l.cl, nil
		}
		l.cl.Close()
		l.cl = l.base.Pinned()
	}
	return nil, fmt.Errorf("transform: no connection to replica %s", l.host)
}

func closeLanes(lanes []*lane) {
	for _, l := range lanes {
		l.cl.Close()
	}
}
