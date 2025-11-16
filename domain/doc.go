// Package domain defines the core business logic and data structures of the Marasi application.
// It contains the primary domain models, such as ProxyRequest, ProxyResponse, and Launchpad,
// as well as the repository interfaces that define the contracts for data persistence.
//
// This package serves as the central point for application-wide types and business rules,
// ensuring a clean separation between the application's core logic and its implementation details,
// such as the database, UI, or external services. By defining interfaces for repositories,
// the domain package remains independent of the data storage technology.
package domain
