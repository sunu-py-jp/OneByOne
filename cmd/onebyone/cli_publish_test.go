package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"onebyone/internal/model"
)

type publicationCLIFixture struct {
	preview      model.ResultPublicationPreview
	request      model.PublishResultsRequest
	previewCalls int
	publishCalls int
	previewError error
	diffRequest  []string
}

func (s *publicationCLIFixture) GetResultPublicationPreview() (model.ResultPublicationPreview, error) {
	s.previewCalls++
	return s.preview, s.previewError
}

func (s *publicationCLIFixture) PublishResults(request model.PublishResultsRequest) (model.ResultPublication, error) {
	s.publishCalls++
	s.request = request
	return model.ResultPublication{Branch: request.Branch, Title: request.Title, Message: request.Message}, nil
}

func (s *publicationCLIFixture) GetResultPublicationFileDiff(workspaceID, revision, file string) (string, error) {
	s.diffRequest = []string{workspaceID, revision, file}
	return "-before\n+after\n", nil
}

func TestCLIPublishDiffUsesReviewedRevisionAndDoesNotPublish(t *testing.T) {
	s := &publicationCLIFixture{}
	result, err := executePublishCommand(context.Background(), s, []string{"diff", "--input", "-"}, strings.NewReader(`{"workspaceId":"w","revision":"r","file":"src/save.js"}`))
	if err != nil || len(s.diffRequest) != 3 || strings.Join(s.diffRequest, "|") != "w|r|src/save.js" || s.publishCalls != 0 {
		t.Fatalf("incorrect diff routing: %+v %v", s.diffRequest, err)
	}
	var out bytes.Buffer
	if err := printCLIResult(&out, result, false); err != nil || out.String() != "-before\n+after\n" {
		t.Fatal("human output did not show the plain diff")
	}
	for _, body := range []string{`{}`, `{"workspaceId":"w","file":"a"}`, `{"workspaceId":"w","revision":"r"}`, `{"workspaceId":"w","revision":"r","file":"a","title":"extra"}`} {
		if _, err := executePublishCommand(context.Background(), s, []string{"diff", "--input", "-"}, strings.NewReader(body)); err == nil {
			t.Fatalf("invalid diff input accepted: %s", body)
		}
	}
}

func TestCLIPublishRequiresReviewedResultAndUsesDefaultSummary(t *testing.T) {
	s := &publicationCLIFixture{preview: model.ResultPublicationPreview{WorkspaceID: "workspace-a", Revision: "reviewed-1", Message: "src/save.js\n- R019: 保存処理を非同期化"}}
	result, err := executePublishCommand(context.Background(), s, []string{"preview"}, nil)
	if err != nil || result.(model.ResultPublicationPreview).Revision != "reviewed-1" || s.publishCalls != 0 {
		t.Fatalf("preview changed results: %v", err)
	}
	const request = `{"workspaceId":"workspace-a","revision":"reviewed-1","branch":"review/storage","title":"保存APIを更新"}`
	result, err = executePublishCommand(context.Background(), s, []string{"create", "--input", "-"}, strings.NewReader(request))
	if err != nil || s.publishCalls != 1 || s.request.Message != s.preview.Message || s.request.Title != "保存APIを更新" || result.(model.ResultPublication).Branch != "review/storage" {
		t.Fatalf("create did not preserve reviewed details and default summary: %+v: %v", s.request, err)
	}
	for _, message := range []string{`""`, `"自分で編集した本文"`} {
		body := strings.TrimSuffix(request, "}") + `,"message":` + message + `}`
		calls := s.previewCalls
		if _, err := executePublishCommand(context.Background(), s, []string{"create", "--input", "-"}, strings.NewReader(body)); err != nil {
			t.Fatal(err)
		}
		if s.previewCalls != calls || s.request.Message != strings.Trim(message, `"`) {
			t.Fatal("explicit commit body was replaced with the generated summary")
		}
	}
	s.preview.Revision = "changed-2"
	before := s.publishCalls
	if _, err := executePublishCommand(context.Background(), s, []string{"create", "--input", "-"}, strings.NewReader(request)); err == nil || s.publishCalls != before {
		t.Fatal("unreviewed revision was published when generating the default summary")
	}
	s.preview.Revision, s.preview.WorkspaceID = "reviewed-1", "workspace-b"
	if _, err := executePublishCommand(context.Background(), s, []string{"create", "--input", "-"}, strings.NewReader(request)); err == nil || s.publishCalls != before {
		t.Fatal("default summary was borrowed from another workspace")
	}
}

func TestCLIPublishInvalidInputAndCancellationCannotCreateBranch(t *testing.T) {
	s := &publicationCLIFixture{}
	for _, body := range []string{
		`{}`, `null`, `{"workspaceId":"w","revision":"r","branch":"b","title":" "}`,
		`{"workspaceId":"w","revision":"r","branch":"","title":"title"}`,
		`{"revision":"r","branch":"b","title":"title"}`,
		`{"workspaceId":"w","branch":"b","title":"title"}`,
		`{"workspaceId":"w","revision":"r","branch":"b","title":"title","force":true}`,
	} {
		if _, err := executePublishCommand(context.Background(), s, []string{"create", "--input", "-"}, strings.NewReader(body)); err == nil {
			t.Errorf("invalid publication accepted: %s", body)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := executePublishCommand(ctx, s, []string{"create", "--input", "-"}, strings.NewReader(`{}`)); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled publication was not stopped")
	}
	if s.publishCalls != 0 || s.previewCalls != 0 {
		t.Fatal("invalid publication reached the backend")
	}
}

func TestCLIPublishHelpAndFlagValidationDoNotInitializeStorage(t *testing.T) {
	base := t.TempDir()
	t.Setenv("ONEBYONE_PRIVATE_DIR", filepath.Join(base, "private"))
	config := filepath.Join(base, "not-created", "app-settings.json")
	for _, args := range [][]string{
		{"publish", "--help"}, {"publish", "create", "--help=false"},
		{"publish", "create"}, {"publish", "preview", "--input", "-"},
		{"publish", "create", "--input", "-", "--force"}, {"publish", "overwrite"},
	} {
		var out, logs bytes.Buffer
		code := executeWithInput(append([]string{"--config", config}, args...), nil, &out, &logs)
		if strings.Contains(strings.Join(args, " "), "--help") {
			if code != 0 || !strings.Contains(out.String(), "publish") {
				t.Fatalf("publication help failed: %d %s", code, logs.String())
			}
		} else if code != 1 {
			t.Fatalf("invalid flags accepted: %v", args)
		}
		if _, err := os.Stat(filepath.Dir(config)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("help/invalid publication created app storage")
		}
	}
	if _, err := executePublishCommand(context.Background(), nil, []string{"create", "--help=false"}, nil); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help flag did not stop before service access: %v", err)
	}
	ctx := context.WithValue(context.Background(), cliArgumentValidationKey{}, true)
	if _, err := executePublishCommand(ctx, nil, []string{"create", "--input", "-"}, nil); err != nil {
		t.Fatal("argument preflight read input or initialized service:", err)
	}
}
