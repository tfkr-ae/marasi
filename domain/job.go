package domain

// JobName names a background job (docs/adr/2026-10-14-background-jobs-save-their-state.md).
type JobName string

const (
	// JobTrafficIndex builds the traffic index.
	JobTrafficIndex JobName = "traffic_index"
	// JobAnnotationIndex backfills the annotation index, which holds notes
	// and metadata (docs/adr/2026-10-14-search-index-splits-annotations-from-bodies.md).
	JobAnnotationIndex JobName = "annotation_index"
	// JobSitemap adds pairs to the stored sitemap (docs/adr/2026-10-14-sitemap-is-stored-by-node-and-content-type.md).
	JobSitemap JobName = "sitemap"
	// JobPrettifiedMove moves prettified copies out of metadata (docs/adr/2026-10-14-prettified-bodies-leave-metadata.md).
	JobPrettifiedMove JobName = "prettified_move"
	// JobInterceptedCount counts the intercepted pairs captured before the
	// dashboard's maintained counts existed.
	JobInterceptedCount JobName = "intercepted_count"
)

// SearchIndexJobs are the jobs text search depends on. A traffic read's
// index.complete is true when all of them are complete.
var SearchIndexJobs = []JobName{JobTrafficIndex, JobAnnotationIndex}
