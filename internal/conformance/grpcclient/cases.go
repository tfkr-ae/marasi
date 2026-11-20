package grpcclient

// Client is the interop client that runs a case.
type Client string

// Interop clients.
const (
	GoClient   Client = "grpc-go"
	JavaClient Client = "grpc-java"
)

// Case is one interop test case.
type Case struct {
	Name   string // the interop test_case, and the case's name in its ID
	Client Client
}

// Cases are the suite's cases, in the order it runs them. grpc-go's
// interop client runs every case it implements. It has none of the
// compressed cases, so grpc-java's interop client runs those, against the
// same server through the same shim.
var Cases = []Case{
	{"empty_unary", GoClient},
	{"large_unary", GoClient},
	{"client_streaming", GoClient},
	{"server_streaming", GoClient},
	{"ping_pong", GoClient},
	{"empty_stream", GoClient},
	{"timeout_on_sleeping_server", GoClient},
	{"cancel_after_begin", GoClient},
	{"cancel_after_first_response", GoClient},
	{"custom_metadata", GoClient},
	{"status_code_and_message", GoClient},
	{"special_status_message", GoClient},
	{"unimplemented_method", GoClient},
	{"unimplemented_service", GoClient},
	{"client_compressed_unary", JavaClient},
	{"server_compressed_unary", JavaClient},
	{"client_compressed_streaming", JavaClient},
	{"server_compressed_streaming", JavaClient},
}
