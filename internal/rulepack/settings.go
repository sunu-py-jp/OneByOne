// Package rulepack reads and writes portable .oborules archives. It never runs
// programs, modifies workspace selection, or includes personal LLM settings.
package rulepack

import (
	"fmt"
	"strings"

	"onebyone/internal/model"
)

// Settings is an explicit allowlist. Do not embed model.Config: root, queue,
// execution limits, prices, connection selection and credentials belong to the
// receiving workspace/user.
type Settings struct {
	IncludeGlobs  []string        `json:"includeGlobs"`
	ExcludeGlobs  []string        `json:"excludeGlobs"`
	CheckCommands []model.Command `json:"checkCommands"`
}

func FromConfig(c model.Config) Settings {
	return cloneSettings(Settings{IncludeGlobs: c.IncludeGlobs, ExcludeGlobs: c.ExcludeGlobs, CheckCommands: c.CheckCommands})
}

// Apply changes only transformation settings; callers assign extracted rules and
// legacy paths after successfully installing their own managed resource copy.
func (s Settings) Apply(c model.Config) model.Config {
	s = cloneSettings(s)
	c.IncludeGlobs, c.ExcludeGlobs, c.CheckCommands = s.IncludeGlobs, s.ExcludeGlobs, s.CheckCommands
	return c
}

func cloneSettings(s Settings) Settings {
	s.IncludeGlobs = append([]string{}, s.IncludeGlobs...)
	s.ExcludeGlobs = append([]string{}, s.ExcludeGlobs...)
	s.CheckCommands = append([]model.Command{}, s.CheckCommands...)
	for i := range s.CheckCommands {
		s.CheckCommands[i].Args = append([]string{}, s.CheckCommands[i].Args...)
	}
	return s
}

func (s Settings) validate() error {
	for _, list := range [][]string{s.IncludeGlobs, s.ExcludeGlobs} {
		for _, glob := range list {
			if strings.ContainsAny(glob, "\x00\r\n") {
				return fmt.Errorf("対象の絞り込み条件が不正です")
			}
		}
	}
	for _, command := range s.CheckCommands {
		if strings.TrimSpace(command.Executable) == "" || strings.ContainsAny(command.Executable, "\x00\r\n") || strings.ContainsRune(command.Name, 0) {
			return fmt.Errorf("検証コマンドの指定が不正です")
		}
		for _, arg := range command.Args {
			if strings.ContainsRune(arg, 0) {
				return fmt.Errorf("検証コマンドの引数が不正です")
			}
		}
	}
	return nil
}
