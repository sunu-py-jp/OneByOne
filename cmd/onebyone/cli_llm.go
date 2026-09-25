package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"onebyone/internal/engine"
	"onebyone/internal/model"
)

// executeLLMCommand returns only safe engine snapshots. Connection secrets are
// accepted through a JSON file/stdin, never flags that enter shell history or
// process listings. Signal cancellation is owned by the top-level dispatcher.
func executeLLMCommand(ctx context.Context, s *engine.Service, args []string, in io.Reader) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(args) == 0 {
		return nil, errors.New("llm の操作を指定してください: list / save / select / delete / test / login / logout / clear")
	}
	command := args[0]
	flags := flag.NewFlagSet("onebyone llm "+command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var id, input string
	var yes bool
	switch command {
	case "list":
	case "save":
		flags.StringVar(&input, "input", "", "LLM接続のJSONファイル（-で標準入力）")
	case "select", "test", "login", "logout":
		flags.StringVar(&id, "id", "", "LLM接続ID")
	case "delete", "clear":
		flags.StringVar(&id, "id", "", "LLM接続ID")
		flags.BoolVar(&yes, "yes", false, "削除を確定")
	default:
		return nil, errors.New("不明な llm 操作です。onebyone llm --help を参照してください")
	}
	if err := flags.Parse(args[1:]); err != nil {
		return nil, err
	}
	if flags.NArg() != 0 {
		return nil, errors.New("余分な引数があります。認証情報は llm save --input のJSONで指定してください")
	}
	if command == "save" && strings.TrimSpace(input) == "" {
		return nil, errors.New("llm save には --input PATH が必要です（標準入力は --input -）")
	}
	if command != "list" && command != "save" && strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("llm %s には --id が必要です", command)
	}
	if (command == "delete" || command == "clear") && !yes {
		return nil, fmt.Errorf("llm %s を確定するには --yes を指定してください", command)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if validatingCLIArguments(ctx) {
		return nil, nil
	}
	id = strings.TrimSpace(id)
	switch command {
	case "list":
		return s.Snapshot().LLMConnections, nil
	case "save":
		var connection model.LLMConnection
		if err := readCLIJSON(in, input, &connection); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return s.SaveLLMConnection(connection)
	case "select":
		return s.SelectLLMConnection(id)
	case "delete":
		return s.DeleteLLMConnection(id)
	case "test":
		message, err := s.TestLLMConnection(id)
		if err != nil {
			return nil, err
		}
		return map[string]string{"message": message}, nil
	case "login":
		return s.SignInLLMConnection(id)
	case "logout":
		for _, connection := range s.Snapshot().LLMConnections {
			if connection.ID == id && connection.AuthMode != "oauth" {
				return nil, errors.New("logout はOAuth接続に使用します。APIキーなどの認証情報を削除するには llm clear --id ID --yes を使用してください")
			}
		}
		return s.SignOutLLMConnection(id)
	case "clear":
		return s.ClearLLMConnectionCredential(id)
	}
	return nil, errors.New("LLM接続の操作を実行できませんでした")
}
