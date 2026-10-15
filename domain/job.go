package domain

// JobName names a background job (ADR-0039).
type JobName string

const (
	// JobTrafficIndex builds the traffic index.
	JobTrafficIndex JobName = "traffic_index"
	// JobAnnotationIndex backfills the annotation index, which holds notes
	// and metadata (ADR-0038).
	JobAnnotationIndex JobName = "annotation_index"
	// JobSitemap adds pairs to the stored sitemap (ADR-0040).
	JobSitemap JobName = "sitemap"
	// JobPrettifiedMove moves prettified copies out of metadata (ADR-0036).
	JobPrettifiedMove JobName = "prettified_move"
)

// SearchIndexJobs are the jobs text search depends on. A traffic read's
// index.complete is true when all of them are complete.
var SearchIndexJobs = []JobName{JobTrafficIndex, JobAnnotationIndex}
