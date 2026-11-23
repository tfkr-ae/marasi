package marasi

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi/armory"
	"github.com/tfkr-ae/marasi/domain"
)

// CreateArmoryTemplateFromPair builds an Armory template from pair id's
// request and stores it. The template is named "METHOD host path" and holds
// the joined request, escaped so it renders back to the captured text
// (docs/adr/2026-11-23-armory-templates-name-their-framing-and-hold-messages-as-text-forms.md).
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
	if err := repo.CreateArmoryTemplate(template); err != nil {
		return nil, fmt.Errorf("storing the Armory template: %w", err)
	}
	return template, nil
}
