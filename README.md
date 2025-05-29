<div align="center">

# Marasi

[![GoDoc](https://godoc.org/github.com/tfkr-ae/marasi?status.png)](https://godoc.org/github.com/tfkr-ae/marasi)

<img src="images/logo.svg" width="150" alt="Marasi">

A Go library for building application security testing proxies.

[marasi.app](https://marasi.app)
</div>

## Features

- **HTTP/HTTPS Proxy**: TLS-capable proxy server with certificate management
- **Request/Response Interception**: Modify traffic in real-time
- **Lua Extensions**: Scriptable proxy behavior with built-in extensions
- **Application Data Format**: SQLite-based storage for all proxy data (requests, responses, metadata)
- **Launchpad**: Replay and modify HTTP requests
- **Scope Management**: Filter traffic with inclusion/exclusion rules
- **Waypoints**: Override hostnames for request routing
- **Chrome Integration**: Auto-configure Chrome with proxy settings

## Core Components

### Proxy Engine
- Built on [Google Martian Proxy](https://github.com/google/martian)
- Automatic content decoding
- Content prettification (JSON, XML, HTML)
- Concurrent request/response processing

### Extension System
Lua based extension support, three built-in extensions:
- **Compass**: Scope management and traffic filtering
- **Checkpoint**: Request/response interception rules
- **Workshop**: Lua development environment

The ability to add custom extensions coming soon.


### Application File Format
SQLite-based application file format stores:
- Complete request/response data with timing
- Extension management and settings
- Launchpad entries for request replay
- Comprehensive logging system
- User notes and hostname waypoints

## Library Usage

This library is designed for building web appilcation security proxies. 

Basic integration (for full implementation see marasi-app):

```go
// Create proxy with options
proxy, err := marasi.New(
    marasi.WithConfigDir("/path/to/config"),
    marasi.WithDatabase(repo),
)

// Set up handlers for your application
proxy.WithOptions(
    marasi.WithRequestHandler(func(req marasi.ProxyRequest) error {
        // Handle Requests in your application
        return nil
    }),
    marasi.WithResponseHandler(func(res marasi.ProxyResponse) error {
        // Handle Responses in your application
        return nil
    }),
    marasi.WithLogHandler(func(logItem marasi.Log) error {
        // Handle log messages
        return nil
    }),
    marasi.WithInterceptHandler(func(intercepted *marasi.Intercepted) error {
        switch intercepted.Type {
        case "request":
        // Handle Request interception
        case "response":
        // Handle Response interception
        }
        return nil
    }),
)

// Start proxy server
listener, err := proxy.GetListener("127.0.0.1", "8080")
proxy.Serve(listener)
```
# GitHub Discussion
Use the [GitHub Discussion](https://github.com/tfkr-ae/marasi/discussions) to provide feedback, ask questions or discuss the project.