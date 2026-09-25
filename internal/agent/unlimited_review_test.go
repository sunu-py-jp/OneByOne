package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"onebyone/internal/model"
)

func TestUnlimitedIndependentReviewCountsContinuePastOldLimits(t *testing.T) {
	in, state, final := reviewGateFixture()
	in.Config.MaxAttempts, in.Config.MaxTurns = 0, 0
	state.Usage.Turns, state.ReviewCount = 40, 4
	in.ReviewCandidate = passedTestReview
	checkpoint := func() error { return nil }
	for index := 0; index < 5; index++ {
		id := fmt.Sprintf("new-candidate-%d", index)
		state.LastCandidate.Result.CandidateID = id
		state.LastCandidate.Review = nil
		final.CandidateID = id
		out, done, err := reviewFinal(context.Background(), in, &state, final, checkpoint, checkpoint)
		if err != nil || !done || out.Outcome != "modified" {
			t.Fatalf("unlimited review stopped at iteration %d: %+v, %v", index, out, err)
		}
	}
	if state.ReviewCount != 9 || state.Usage.Turns != 45 {
		t.Fatalf("unlimited reviews reset or lost durable usage: %+v", state)
	}
}

func TestExplicitIndependentReviewCapsStillStopBeforeRequest(t *testing.T) {
	for _, kind := range []string{"attempts", "turns"} {
		t.Run(kind, func(t *testing.T) {
			in, state, final := reviewGateFixture()
			state.LastCandidate.Review = nil
			in.Config.MaxAttempts, in.Config.MaxTurns = 0, 0
			if kind == "attempts" {
				in.Config.MaxAttempts, state.ReviewCount = 2, 2
			} else {
				in.Config.MaxTurns, state.Usage.Turns = 7, 7
			}
			called := false
			in.ReviewCandidate = func(context.Context, ReviewInput) (model.IndependentReview, error) {
				called = true
				return model.IndependentReview{}, nil
			}
			checkpoint := func() error { return nil }
			if _, _, err := reviewFinal(context.Background(), in, &state, final, checkpoint, checkpoint); err == nil || called {
				t.Fatal("explicit review cap did not stop before a request")
			}
		})
	}
}

func TestUnlimitedReviewTransportAcceptsHighTurnCountAndNoDeadline(t *testing.T) {
	var input ReviewInput
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		respond(w, reviewResponseItem(reviewWire(input, "passed")))
	}))
	defer srv.Close()
	input = reviewTestInput(srv.URL)
	input.Config.MaxTurns, input.Config.TimeoutSeconds = 0, 0
	input.Usage.Turns = 50
	var messages []string
	input.Log = func(message string) { messages = append(messages, message) }
	client, err := newClient(input.Config)
	if err != nil {
		t.Fatal(err)
	}
	defer client.http.CloseIdleConnections()
	if client.http.Timeout != 0 || client.http.Transport.(*http.Transport).ResponseHeaderTimeout != 0 {
		t.Fatal("unlimited review still has a request timeout")
	}
	out, err := Review(context.Background(), input)
	if err != nil || out.Verdict != "passed" || requests != 1 {
		t.Fatalf("unlimited review with historical turns did not complete: %+v, %v", out, err)
	}
	if joined := strings.Join(messages, "\n"); !strings.Contains(joined, "turn 51 (no limit)") || strings.Contains(joined, "/0") {
		t.Fatalf("unlimited review logged a zero-turn cap: %s", joined)
	}
}

func TestReviewTransportUnlimitedCancellationAndPositiveTimeout(t *testing.T) {
	for _, seconds := range []int{0, 1} {
		t.Run(fmt.Sprint(seconds), func(t *testing.T) {
			started := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
				_, _ = io.Copy(io.Discard, request.Body)
				close(started)
				<-request.Context().Done()
			}))
			defer srv.Close()
			input := reviewTestInput(srv.URL)
			input.Config.MaxTurns, input.Config.TimeoutSeconds = 0, seconds
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			finished := make(chan error, 1)
			go func() { _, err := Review(ctx, input); finished <- err }()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				cancel()
				t.Fatal("review did not start")
			}
			if seconds == 0 {
				cancel()
			}
			select {
			case err := <-finished:
				if err == nil || !IsUsageUnknown(err) {
					t.Fatalf("cancelled/timed-out in-flight review lost uncertain usage: %v", err)
				}
			case <-time.After(3 * time.Second):
				cancel()
				t.Fatal("review ignored cancellation or a configured timeout")
			}
		})
	}
}
