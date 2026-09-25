package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"path/filepath"
	"strings"

	"onebyone/internal/model"
)

type resultPublicationService interface {
	GetResultPublicationPreview() (model.ResultPublicationPreview, error)
	GetResultPublicationFileDiff(workspaceID, revision, file string) (string, error)
	PublishResults(model.PublishResultsRequest) (model.ResultPublication, error)
}

func executePublishCommand(ctx context.Context, s resultPublicationService, args []string, in io.Reader) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(args) == 0 {
		return nil, errors.New("publish preview / diff / create を指定してください")
	}
	if args[0] != "preview" && args[0] != "diff" && args[0] != "create" {
		return nil, errors.New("不明なpublish操作です。--help を参照してください")
	}
	var input string
	if err := commandFlags("publish "+args[0], args[1:], func(f *flag.FlagSet) {
		if args[0] != "preview" {
			f.StringVar(&input, "input", "", "反映内容のJSONファイルまたは -")
		}
	}); err != nil {
		return nil, err
	}
	if args[0] != "preview" && strings.TrimSpace(input) == "" {
		return nil, errors.New("--input を指定してください。先に publish preview --json で反映内容を確認してください")
	}
	if validatingCLIArguments(ctx) {
		return nil, nil
	}
	if args[0] == "preview" {
		return s.GetResultPublicationPreview()
	}
	if args[0] == "diff" {
		var request struct {
			WorkspaceID string `json:"workspaceId"`
			Revision    string `json:"revision"`
			File        string `json:"file"`
		}
		if err := readCLIJSON(in, input, &request); err != nil {
			return nil, err
		}
		if strings.TrimSpace(request.WorkspaceID) == "" || strings.TrimSpace(request.Revision) == "" || strings.TrimSpace(request.File) == "" {
			return nil, errors.New("publish preview で確認した workspaceId・revision と file（相対パス）を指定してください")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		diff, err := s.GetResultPublicationFileDiff(request.WorkspaceID, request.Revision, filepath.ToSlash(request.File))
		return map[string]string{"diff": diff}, err
	}
	// A pointer distinguishes an omitted message (use the reviewed summary) from
	// an intentionally empty message. The commit title always needs user input.
	var inputRequest struct {
		WorkspaceID string  `json:"workspaceId"`
		Revision    string  `json:"revision"`
		Branch      string  `json:"branch"`
		Title       string  `json:"title"`
		Message     *string `json:"message"`
	}
	if err := readCLIJSON(in, input, &inputRequest); err != nil {
		return nil, err
	}
	if strings.TrimSpace(inputRequest.WorkspaceID) == "" || strings.TrimSpace(inputRequest.Revision) == "" {
		return nil, errors.New("publish preview で確認した workspaceId と revision を指定してください")
	}
	if strings.TrimSpace(inputRequest.Branch) == "" || strings.TrimSpace(inputRequest.Title) == "" {
		return nil, errors.New("branch（新規ブランチ名）と title（コミットタイトル）は必須です")
	}
	request := model.PublishResultsRequest{
		WorkspaceID: inputRequest.WorkspaceID, Revision: inputRequest.Revision,
		Branch: inputRequest.Branch, Title: inputRequest.Title,
	}
	if inputRequest.Message == nil {
		preview, err := s.GetResultPublicationPreview()
		if err != nil {
			return nil, err
		}
		if preview.WorkspaceID != request.WorkspaceID || preview.Revision != request.Revision {
			return nil, errors.New("反映内容が変更されています。publish preview を再取得して確認してください")
		}
		request.Message = preview.Message
	} else {
		request.Message = *inputRequest.Message
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.PublishResults(request)
}
