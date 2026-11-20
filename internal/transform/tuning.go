package transform

import "fmt"

// Tuning sizes a build's work to the ClickHouse servers it runs on. The
// defaults suit ClickHouse Cloud replicas of 8 cores and 32 GB.
type Tuning struct {
	// BatchStudies is how many studies a full build fills a table with at
	// a time: a portal with more studies is filled batch by batch, so a
	// query needs the memory of one batch, not of every study. On all of
	// datahub, a batch of 50 studies needs up to about 14 GB.
	BatchStudies int
	// InsertThreads is how many threads write the rows of each insert
	// (ClickHouse's max_insert_threads), about one per core.
	InsertThreads int
	// Replicas is how many replicas a full build fills a table's batches
	// on at once, one insert per replica (replicas.go). 1 fills one batch
	// at a time.
	Replicas int
	// StudyFills is how many tables a study build (one or a few changed
	// studies) fills at once: each insert is small, and most of its time
	// is spent waiting for the replicas.
	StudyFills int
}

// DefaultTuning is the tuning a build uses unless SetTuning changes it.
var DefaultTuning = Tuning{BatchStudies: 50, InsertThreads: 8, Replicas: 2, StudyFills: 4}

var tuning = DefaultTuning

// SetTuning makes the builds that start from now on use t.
func SetTuning(t Tuning) error {
	if t.BatchStudies < 1 || t.InsertThreads < 1 || t.Replicas < 1 || t.StudyFills < 1 {
		return fmt.Errorf("transform: every tuning value must be at least 1, got %+v", t)
	}
	tuning = t
	return nil
}
