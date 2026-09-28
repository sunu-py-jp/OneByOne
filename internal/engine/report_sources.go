package engine

import (
	"os"
	"path/filepath"
	"strings"

	"onebyone/internal/catalog"
	"onebyone/internal/model"
)

// Source positions supplied by either model become renderer evidence only
// after matching immutable, hash-checked attempt artifacts. Missing evidence
// keeps the explanation at file level; it never authorizes a guessed location.
type reportSources struct {
	before, after           string
	beforeValid, afterValid bool
}

func buildRecordedChangeReport(cfg model.Config, h model.Attempt, c *repairCheckpoint) []model.ChangeReportItem {
	return buildChangeReport(h, c, loadReportSources(cfg, h, c))
}

func loadReportSources(cfg model.Config, h model.Attempt, c *repairCheckpoint) reportSources {
	var sources reportSources
	if c == nil || c.AttemptID != h.ID || c.InputHash != h.InputHash || c.BaseCommit != h.BaseCommit {
		return sources
	}
	statePath, err := repairPath(cfg, h.ID)
	if err != nil {
		return sources
	}
	dir := filepath.Dir(statePath)
	sources.before, sources.beforeValid = readReportSource(dir, h.ID+".before", h.InputHash)
	candidate := c.State.LastCandidate
	if !sources.beforeValid || candidate == nil || candidate.Request.BaseHash != h.InputHash || candidate.Result.CandidateHash == "" {
		return sources
	}
	if candidate.NoChange {
		if candidate.Result.CandidateHash == h.InputHash && (h.OutputHash == "" || h.OutputHash == h.InputHash) {
			sources.after, sources.afterValid = sources.before, true
		}
		return sources
	}
	if candidate.Result.CandidateHash != h.OutputHash {
		return sources
	}
	id := candidate.Result.CandidateID
	if id == "" || filepath.Base(id) != id || strings.ContainsAny(id, "/\\:*?\"<>|\x00\r\n") || id == "." || id == ".." {
		return sources
	}
	sources.after, sources.afterValid = readReportSource(dir, h.ID+".candidate-"+id+".after", candidate.Result.CandidateHash)
	return sources
}

func readReportSource(dir, name, hash string) (string, bool) {
	if hash == "" {
		return "", false
	}
	path, err := catalog.PathWithin(dir, name)
	if err != nil {
		return "", false
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil || digest(data) != hash {
		return "", false
	}
	source, err := decodeSource(data)
	if err != nil {
		return "", false
	}
	return source.text, true
}

func reportLocationRange(source string, valid bool, location model.SourceLocation, after bool) (model.ChangeLineRange, bool) {
	if !valid || model.ValidateSourceLocation(source, location) != nil {
		return model.ChangeLineRange{}, false
	}
	if after {
		return model.ChangeLineRange{AfterStart: location.StartLine, AfterEnd: location.EndLine}, true
	}
	return model.ChangeLineRange{BeforeStart: location.StartLine, BeforeEnd: location.EndLine}, true
}
