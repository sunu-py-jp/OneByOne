package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"onebyone/internal/model"
	"testing"
)

func TestAlreadyReviewedCandidateResumesWithoutAnotherRequest(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		respond(w, goodItem())
	}))
	defer srv.Close()
	in, state, _ := reviewGateFixture()
	in.Config.Endpoint, in.Config.Deployment, in.Config.Credential = srv.URL, "mock-model", "test-secret"

	in.RepairState = &state
	in.SaveRepairState = func(model.RepairState) error { return nil }
	out, err := Run(context.Background(), in)
	if err != nil || out.Outcome != "modified" || calls != 0 || out.Usage.Turns != 0 {
		t.Fatalf("already reviewed adoption blocked or billed: out=%+v calls=%d err=%v", out, calls, err)
	}
}
