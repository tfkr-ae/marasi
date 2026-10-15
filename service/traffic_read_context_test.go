package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
)

type requestKey struct{}

// contextTrafficRepository records the value of requestKey in the context
// each read receives.
type contextTrafficRepository struct {
	domain.TrafficRepository
	seen any
}

func (s *contextTrafficRepository) ListTrafficContext(ctx context.Context, _ domain.TrafficQuery) (domain.TrafficPage, error) {
	s.seen = ctx.Value(requestKey{})
	return domain.TrafficPage{}, nil
}

func (s *contextTrafficRepository) CountTrafficContext(ctx context.Context, _ domain.TrafficCountQuery) (domain.TrafficCount, error) {
	s.seen = ctx.Value(requestKey{})
	return domain.TrafficCount{}, nil
}

func (s *contextTrafficRepository) CountContentTypesContext(ctx context.Context) (domain.ContentTypeCounts, error) {
	s.seen = ctx.Value(requestKey{})
	return domain.ContentTypeCounts{}, nil
}

func TestTrafficReadsUseTheRequestContext(t *testing.T) {
	for _, target := range []string{"/traffic", "/traffic/count", "/traffic/content-types"} {
		t.Run("should read "+target+" under the request's context", func(t *testing.T) {
			repo := &contextTrafficRepository{}
			server := newTestServer(&marasi.Proxy{TrafficRepo: repo}, func() {})
			ctx := context.WithValue(context.Background(), requestKey{}, target)

			server.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, target, nil).WithContext(ctx))

			if repo.seen != target {
				t.Fatalf("\nwanted:\nthe request's context\ngot:\n%v", repo.seen)
			}
		})
	}
}
