// Thread-safe statistics collector for pipeline runs.
//
// Each worker increments counters atomically. The final Stats value is read once after
// all workers drain, so no lock contention is possible on the read path.
package pipeline

import "sync/atomic"

// statsCollector accumulates counters atomically across workers.
type statsCollector struct {
	processed  atomic.Int64
	failed     atomic.Int64
	fragEnrich atomic.Int64
	viewsGen   atomic.Int64
	viewsSkip  atomic.Int64
	embeddings atomic.Int64
	tombstones atomic.Int64
}

func (s *statsCollector) addProcessed(n int)         { s.processed.Add(int64(n)) }
func (s *statsCollector) addFailed(n int)            { s.failed.Add(int64(n)) }
func (s *statsCollector) addFragmentsEnriched(n int) { s.fragEnrich.Add(int64(n)) }
func (s *statsCollector) addViewsGenerated(n int)    { s.viewsGen.Add(int64(n)) }
func (s *statsCollector) addViewsSkipped(n int)      { s.viewsSkip.Add(int64(n)) }
func (s *statsCollector) addEmbeddings(n int)        { s.embeddings.Add(int64(n)) }
func (s *statsCollector) addTombstones(n int)        { s.tombstones.Add(int64(n)) }

// snapshot returns the current stats as a value type.
func (s *statsCollector) snapshot() Stats {
	return Stats{
		ItemsProcessed:    int(s.processed.Load()),
		ItemsFailed:       int(s.failed.Load()),
		FragmentsEnrich:   int(s.fragEnrich.Load()),
		ViewsGenerated:    int(s.viewsGen.Load()),
		ViewsSkipped:      int(s.viewsSkip.Load()),
		EmbeddingsStored:  int(s.embeddings.Load()),
		TombstonesApplied: int(s.tombstones.Load()),
	}
}
