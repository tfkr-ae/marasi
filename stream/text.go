package stream

import (
	"mime"
	"strings"
	"unicode/utf8"

	"github.com/tfkr-ae/marasi/protoscope"
)

// TextForm is a stream message's payload in its protobuf text form (GLOSSARY), for the
// direction whose head has contentType. A gRPC, gRPC-Web or Connect
// message whose codec is protobuf, named +proto or not named, is in the
// protoscope text form; one whose codec is JSON is its UTF-8 text. It
// reports false for any other message, which is read as its payload.
//
// The text form is computed on read and never stored
// (docs/adr/2026-11-20-protobuf-is-decoded-for-display-and-re-encoded-only-when-edited.md).
// The control API, Checkpoint and Lua all call this, so they show the same
// text for the same message.
func TextForm(contentType string, payload []byte) (string, bool) {
	switch codecOf(contentType) {
	case codecProto:
		return protoscope.Decode(payload), true
	case codecJSON:
		if utf8.Valid(payload) {
			return string(payload), true
		}
	}
	return "", false
}

// codec is how a gRPC, gRPC-Web or Connect message's payload is encoded.
type codec string

const (
	codecNone  codec = ""
	codecProto codec = "proto"
	codecJSON  codec = "json"
)

// codecOf is the codec contentType names after its +, protobuf when it
// names none. Only the gRPC, gRPC-Web and Connect framings have one.
func codecOf(contentType string) codec {
	switch FramingOf(contentType, false) {
	case FramingGRPC, FramingGRPCWeb, FramingConnect:
	default:
		return codecNone
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType, _, _ = strings.Cut(contentType, ";")
	}
	_, suffix, _ := strings.Cut(strings.ToLower(strings.TrimSpace(mediaType)), "+")
	switch suffix {
	case "", "proto":
		return codecProto
	case "json":
		return codecJSON
	}
	return codecNone
}
