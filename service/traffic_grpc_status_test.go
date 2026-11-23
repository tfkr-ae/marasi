package service

import (
	"maps"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tfkr-ae/marasi"
	"github.com/tfkr-ae/marasi/domain"
)

// grpcStatusBody is the part of a pair read or list item these tests read.
type grpcStatusBody struct {
	ID         uuid.UUID `json:"id"`
	GRPCStatus *int      `json:"grpc_status"`
}

// insertGRPCStatus captures one responded pair whose response recorded
// status as its grpc-status, or none when status is nil, with metadata.
func (p *trafficListProject) insertGRPCStatus(t *testing.T, status *int, metadata map[string]any) uuid.UUID {
	t.Helper()
	time.Sleep(time.Millisecond)
	id := uuid.Must(uuid.NewV7())
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := p.repo.InsertRequest(&domain.ProxyRequest{ID: id, Scheme: "https", Method: "POST", Host: "example.com", Path: "/svc/Method",
		Raw: []byte("POST /svc/Method HTTP/2\r\n\r\n"), Metadata: metadata, RequestedAt: now, ClientProtocol: domain.ProtocolHTTP2}); err != nil {
		t.Fatalf("inserting request: %v", err)
	}
	if err := p.repo.InsertResponse(&domain.ProxyResponse{ID: id, Status: "200 OK", StatusCode: 200, ContentType: "application/grpc",
		Raw: []byte{}, Metadata: map[string]any{}, RespondedAt: now, GRPCStatus: status}); err != nil {
		t.Fatalf("inserting response: %v", err)
	}
	return id
}

func grpcStatusOf(items []grpcStatusBody) map[uuid.UUID]int {
	got := map[uuid.UUID]int{}
	for _, item := range items {
		if item.GRPCStatus == nil {
			got[item.ID] = -1
			continue
		}
		got[item.ID] = *item.GRPCStatus
	}
	return got
}

func TestTrafficGRPCStatus(t *testing.T) {
	invalidArgument, permissionDenied := 3, 7

	t.Run("should show a pair's grpc-status in a pair read and the traffic list, and none for a pair that is not gRPC", func(t *testing.T) {
		p := setupTrafficListProject(t)
		three := p.insertGRPCStatus(t, &invalidArgument, map[string]any{})
		seven := p.insertGRPCStatus(t, &permissionDenied, map[string]any{})
		plain := p.insertGRPCStatus(t, nil, map[string]any{})

		var list struct {
			Items []grpcStatusBody `json:"items"`
		}
		p.getJSON(t, "/traffic", &list)
		want := map[uuid.UUID]int{three: 3, seven: 7, plain: -1}
		if got := grpcStatusOf(list.Items); !maps.Equal(got, want) {
			t.Fatalf("traffic list\nwanted:\n%v\ngot:\n%v", want, got)
		}
		for id, status := range want {
			var pair grpcStatusBody
			p.getJSON(t, "/traffic/"+id.String(), &pair)
			if got := grpcStatusOf([]grpcStatusBody{pair})[id]; got != status {
				t.Fatalf("pair read %s\nwanted:\n%d\ngot:\n%d", id, status, got)
			}
		}
	})

	t.Run("should select only the pairs with a grpc-status in a query", func(t *testing.T) {
		p := setupTrafficListProject(t)
		three := p.insertGRPCStatus(t, &invalidArgument, map[string]any{})
		seven := p.insertGRPCStatus(t, &permissionDenied, map[string]any{})
		p.insertGRPCStatus(t, nil, map[string]any{})

		for query, want := range map[string][]uuid.UUID{
			`grpc_status = 3`:  {three},
			`grpc_status != 3`: {seven},
			`grpc_status > 0`:  {three, seven},
			`grpc_status = 0`:  nil,
		} {
			_, body := p.get(t, "/traffic?q="+url.QueryEscape(query))
			got := body.ids()
			slices.SortFunc(got, func(a, b uuid.UUID) int { return compareUUID(a, b) })
			if !slices.Equal(got, want) {
				t.Fatalf("%s\nwanted:\n%v\ngot:\n%v", query, want, got)
			}
		}
	})

	t.Run("should show grpc-status in an Armory run's traffic and select the run's pairs by it", func(t *testing.T) {
		p := setupTrafficListProject(t)
		templateID, runID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
		if err := p.repo.CreateArmoryTemplate(&domain.ArmoryTemplate{ID: templateID, Name: "grpc", RawTemplate: "POST / HTTP/2\n"}); err != nil {
			t.Fatalf("creating template: %v", err)
		}
		if err := p.repo.CreateArmoryRun(&domain.ArmoryRun{ID: runID, TemplateID: templateID, TemplateSnapshot: "POST / HTTP/2\n",
			Status: domain.ArmoryRunCompleted, AttackType: domain.ArmoryAttackHarpoon, MaxConcurrent: 1}); err != nil {
			t.Fatalf("creating run: %v", err)
		}
		inRun := map[string]any{"armory_run_id": runID.String()}
		three := p.insertGRPCStatus(t, &invalidArgument, inRun)
		seven := p.insertGRPCStatus(t, &permissionDenied, inRun)
		p.insertGRPCStatus(t, &invalidArgument, map[string]any{})
		for _, id := range []uuid.UUID{three, seven} {
			if err := p.repo.CreateArmoryEntry(&domain.ArmoryEntry{RunID: runID, RequestID: id}); err != nil {
				t.Fatalf("linking pair: %v", err)
			}
		}
		p.server = newTestServer(&marasi.Proxy{Armory: &stubArmoryService{repo: p.repo}, TrafficRepo: p.repo, LaunchpadRepo: p.repo}, func() {})

		var run struct {
			Items []grpcStatusBody `json:"items"`
		}
		p.getJSON(t, "/armory/run/"+runID.String()+"/traffic", &run)
		if got, want := grpcStatusOf(run.Items), map[uuid.UUID]int{three: 3, seven: 7}; !maps.Equal(got, want) {
			t.Fatalf("run traffic\nwanted:\n%v\ngot:\n%v", want, got)
		}

		_, body := p.get(t, "/traffic?q="+url.QueryEscape(`metadata.armory_run_id = "`+runID.String()+`" AND grpc_status = 3`))
		if got := body.ids(); !slices.Equal(got, []uuid.UUID{three}) {
			t.Fatalf("run query\nwanted:\n%v\ngot:\n%v", []uuid.UUID{three}, got)
		}
	})
}
