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
}
