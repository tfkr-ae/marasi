package domain

// StatsRepository defines the interface for retrieving various statistics about the application's data.
// It provides methods for counting different types of entities within the repository.
type StatsRepository interface {
	// CountRows returns the total number of request-response rows stored in the repository.
	CountRows() (int, error)
	// CountNotes returns the total number of notes associated with requests.
	CountNotes() (int, error)
	// CountLaunchpads returns the total number of created launchpads.
	CountLaunchpads() (int, error)
	// CountIntercepted returns the total number of intercepted requests.
	CountIntercepted() (int, error)
	// DashboardCounts returns every dashboard count in one read, from
	// counts the project maintains as it is written.
	DashboardCounts() (DashboardCounts, error)
}

// DashboardCounts are a project's dashboard counts.
type DashboardCounts struct {
	// Pairs is the number of request/response pairs.
	Pairs int
	// Notes is the number of pairs with a non-empty note.
	Notes int
	// Launchpads is the number of launchpads.
	Launchpads int
	// Intercepted is the number of pairs whose metadata has intercepted set
	// to true. Until Complete[JobInterceptedCount] is true it leaves out
	// pairs captured before the project kept this count that the job has
	// not reached yet (ADR-0039).
	Intercepted int
	// Complete reports whether each background job the counts depend on is
	// complete.
	Complete map[JobName]bool
}
