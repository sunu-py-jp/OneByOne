package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"onebyone/internal/model"
)

func TestImportedDiagnosticsReachEditorAndIndependentReview(t *testing.T) {
	in := testInput("")
	in.SourceDiagnostics = []model.SourceDiagnostic{{File: in.File, Line: 1, Code: "CS0619", Message: "Use Modern.Save instead", RuleIDs: []string{"R019"}}}
	turn := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		history := requestHistory(t, r)
		if turn == 0 {
			var payload struct {
				SourceDiagnostics []model.SourceDiagnostic `json:"sourceDiagnostics"`
			}
			entry := history[0].(map[string]any)
			if err := json.Unmarshal([]byte(entry["content"].(string)), &payload); err != nil || !reflect.DeepEqual(payload.SourceDiagnostics, in.SourceDiagnostics) {
				t.Errorf("editor did not receive diagnostics: %+v %v", payload, err)
			}
		}
		respond(w, standardTurn(turn))
		turn++
	}))
	defer srv.Close()
	in.Config.Endpoint = srv.URL
	reviewed := false
	in.ReviewCandidate = func(ctx context.Context, review ReviewInput) (model.IndependentReview, error) {
		reviewed = true
		if !reflect.DeepEqual(review.SourceDiagnostics, in.SourceDiagnostics) {
			t.Error("review lost original diagnostic evidence")
		}
		return passedTestReview(ctx, review)
	}
	result, err := Run(context.Background(), in)
	if err != nil || !reviewed || result.Outcome != "modified" {
		t.Fatalf("result=%+v reviewed=%v err=%v", result, reviewed, err)
	}
}

func TestImportedDiagnosticsSurviveContextRolloverAndPaging(t *testing.T) {
	in := testInput("http://127.0.0.1:1")
	in.SourceDiagnostics = []model.SourceDiagnostic{{File: in.File, Code: "CS0619", Message: strings.Repeat("診断内容", contextPageBytes)}}
	for _, history := range [][]json.RawMessage{repairContext(in, model.RepairState{}, nil, "rollover"), minimalRepairContext(in, model.RepairState{}, "provider limit")} {
		if !strings.Contains(string(history[0]), "sourceDiagnosticCount") || !strings.Contains(string(history[0]), "diagnostics") {
			t.Fatal("rollover forgot diagnostic discovery")
		}
	}
	var joined strings.Builder
	offset := 0
	for {
		output, err := readSavedState(in, model.RepairState{}, fmt.Sprintf(`{"section":"diagnostics","offset":%d}`, offset))
		if err != nil {
			t.Fatal(err)
		}
		var page struct {
			Content    string `json:"content"`
			NextOffset int    `json:"nextOffset"`
			Complete   bool   `json:"complete"`
		}
		if err := json.Unmarshal([]byte(output), &page); err != nil {
			t.Fatal(err)
		}
		joined.WriteString(page.Content)
		if page.Complete {
			break
		}
		if page.NextOffset <= offset {
			t.Fatal("paging stalled")
		}
		offset = page.NextOffset
	}
	var actual []model.SourceDiagnostic
	if err := json.Unmarshal([]byte(joined.String()), &actual); err != nil || !reflect.DeepEqual(actual, in.SourceDiagnostics) {
		t.Fatalf("paged diagnostic evidence was truncated: %v", err)
	}
	review := reviewTestInput("http://127.0.0.1:1")
	review.SourceDiagnostics = in.SourceDiagnostics
	state := newReviewRecoveryState()
	state.markAllRead(review)
	delete(state.read, "catalog")
	if state.completeRead(review) {
		t.Fatal("review approved without reading diagnostic catalog")
	}
	if !strings.Contains(reviewCatalogText(review), "CS0619") || state.manifest(review, true, "")["sourceDiagnosticCount"] != 1 {
		t.Fatal("review paging dropped evidence")
	}
	if !reflect.DeepEqual(state.manifest(review, false, "")["sourceDiagnostics"], review.SourceDiagnostics) {
		t.Fatal("review recovery dropped evidence")
	}
}

func TestReviewRequestIncludesImportedDiagnostics(t *testing.T) {
	var in ReviewInput
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		history := requestHistory(t, r)
		var payload struct {
			SourceDiagnostics []model.SourceDiagnostic `json:"sourceDiagnostics"`
		}
		entry := history[0].(map[string]any)
		if err := json.Unmarshal([]byte(entry["content"].(string)), &payload); err != nil || !reflect.DeepEqual(payload.SourceDiagnostics, in.SourceDiagnostics) {
			t.Errorf("review request lost evidence: %v", err)
		}
		respond(w, reviewResponseItem(reviewWire(in, "passed")))
	}))
	defer srv.Close()
	in = reviewTestInput(srv.URL)
	in.SourceDiagnostics = []model.SourceDiagnostic{{File: in.File, Code: "CS0619", Message: "Old API removed", Line: 1}}
	if _, err := Review(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	in.SourceDiagnostics[0].File = "other.cs"
	if err := validateReviewInput(in, in.Config); err == nil {
		t.Fatal("cross-file diagnostic accepted")
	}
}
