package marasi

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/armory"
	"github.com/tfkr-ae/marasi/domain"
	"github.com/tfkr-ae/marasi/stream"
)

// CreateArmoryTemplateFromPair builds an Armory template from pair id's
// request and stores it. The template is named "METHOD host path" and holds
// the joined request, escaped so it renders back to the captured text
// (docs/adr/2026-11-23-armory-templates-name-their-framing-and-hold-messages-as-text-forms.md).
// A streamed request direction is built as a message-form body: each
// joined request message in its text form under a separator, with the
// framing the messages were sent in. One whose messages have no text form
// is built from the joined request as raw text, with no framing. A Connect
// unary request is built as a message-form body of its one message, with
// the framing connect (armory.ConnectUnaryTemplate).
// It has no payload positions until the tester adds them. A pair that is
// not stored returns an error wrapping domain.ErrPairNotFound.
func (proxy *Proxy) CreateArmoryTemplateFromPair(id uuid.UUID) (*domain.ArmoryTemplate, error) {
	armoryService, err := proxy.GetArmory()
	if err != nil {
		return nil, err
	}
	repo := armoryService.Repo()
	if repo == nil {
		return nil, ErrArmoryRepoNotFound
	}
	trafficRepo, err := proxy.GetTrafficRepo()
	if err != nil {
		return nil, err
	}
	joined, err := proxy.JoinedRequest(id)
	if err != nil {
		return nil, err
	}
	row, err := trafficRepo.GetRequestResponseRow(id)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", domain.ErrPairNotFound, err)
	}
	templateID, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("creating an Armory template ID: %w", err)
	}
	template := &domain.ArmoryTemplate{
		ID:          templateID,
		Name:        fmt.Sprintf("%s %s %s", row.Request.Method, row.Request.Host, row.Request.Path),
		RawTemplate: armory.EscapeTemplate(string(joined.Raw)),
	}
	if framing, payloads, ok := messageFormParts(joined); ok {
		if raw, ok := armory.MessageFormTemplate(string(joined.head), payloads); ok {
			template.RawTemplate = raw
			template.Framing = framing
		}
	} else if raw, ok := armory.ConnectUnaryTemplate(string(joined.Raw)); ok {
		template.RawTemplate = raw
		template.Framing = string(stream.FramingConnect)
	}
	if err := repo.CreateArmoryTemplate(template); err != nil {
		return nil, fmt.Errorf("storing the Armory template: %w", err)
	}
	return template, nil
}

// messageFormParts is the framing and payloads of joined's request messages,
// and false when they cannot be held in a message-form body: the request
// was not streamed, joined no message, or holds a message stored as
// compressed bytes Marasi does not decode.
func messageFormParts(joined JoinedRequest) (string, [][]byte, bool) {
	if !joined.Streamed || len(joined.forwarded) == 0 {
		return "", nil, false
	}
	framing := joined.forwarded[0].Framing
	payloads := make([][]byte, 0, len(joined.forwarded))
	for _, message := range joined.forwarded {
		encoding, _ := message.Metadata["encoding"].(string)
		if message.Framing != framing || (message.Compressed && !stream.Decodable(encoding)) {
			return "", nil, false
		}
		payloads = append(payloads, message.Payload)
	}
	return framing, payloads, true
}
