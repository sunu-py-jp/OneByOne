package main

import (
	"context"
	"errors"
	"flag"
	"strings"

	"onebyone/internal/engine"
)

func executeDiagnosticsCommand(ctx context.Context, s *engine.Service, args []string) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(args) == 0 {
		return nil, errors.New("diagnostics import / clear / show を指定してください")
	}
	var input string
	switch args[0] {
	case "import":
		if err := commandFlags("diagnostics import", args[1:], func(f *flag.FlagSet) { f.StringVar(&input, "input", "", "診断JSONLファイル") }); err != nil {
			return nil, err
		}
		if strings.TrimSpace(input) == "" || input == "-" {
			return nil, errors.New("--input に診断JSONLファイルのパスを指定してください")
		}
	case "clear", "show":
		if err := commandFlags("diagnostics "+args[0], args[1:], func(*flag.FlagSet) {}); err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("不明なdiagnostics操作です。--help を参照してください")
	}
	if validatingCLIArguments(ctx) {
		return nil, nil
	}
	switch args[0] {
	case "import":
		return s.ImportDiagnostics(input)
	case "clear":
		return s.ClearDiagnostics()
	default:
		return s.Snapshot().Diagnostics, nil
	}
}
