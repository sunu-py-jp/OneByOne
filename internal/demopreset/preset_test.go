package demopreset

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"onebyone/internal/ruleformat"
)

func TestProjectAndRulesAreIndependentSnapshots(t *testing.T) {
	files, err := ProjectFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != ProjectFileCount || len(files[".gitignore"]) == 0 {
		t.Fatal("project count or hidden file is missing")
	}
	directories := map[string]bool{}
	targets := 0
	for path := range files {
		if strings.HasPrefix(path, "src/") && strings.HasSuffix(path, ".js") {
			targets++
			directories[filepath.Dir(path)] = true
		}
	}
	if targets != TargetFileCount || len(directories) < 5 {
		t.Fatalf("expected %d targets across five directories, got %d / %d", TargetFileCount, targets, len(directories))
	}
	files[".gitignore"][0] = 'X'
	again, err := ProjectFiles()
	if err != nil || again[".gitignore"][0] == 'X' {
		t.Fatal("project snapshots share mutable bytes")
	}
	pack, err := Rules()
	if err != nil {
		t.Fatal(err)
	}
	if len(pack.Files) != CommonRuleCount+IndividualRuleCount+1 {
		t.Fatal("expected thirteen definitions and the legacy-symbol list")
	}
	pack.Files["rules/R101/rule.json"][0] = 'X'
	pack.Settings.IncludeGlobs[0] = "changed"
	second, err := Rules()
	if err != nil || second.Files["rules/R101/rule.json"][0] == 'X' || second.Settings.IncludeGlobs[0] != "src/**/*.js" {
		t.Fatal("rule snapshots share mutable data")
	}
}

// These checks validate test-only reference implementations, never model output.
// They are intentionally separate from the abstract examples shipped to users.
// Node is optional for the app; skip the reference checks if it is not installed.
func TestReferenceAfterExamples(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is not installed; reference JavaScript checks are optional")
	}
	files, err := ProjectFiles()
	if err != nil {
		t.Fatal(err)
	}
	references := readReferenceSolutions(t)
	if len(references) != IndividualRuleCount+1 {
		t.Fatalf("expected %d test-only reference workflows, got %d", IndividualRuleCount+1, len(references))
	}
	// Keep an executable copy of the input to catch broken SDK imports or source
	// contracts separately from the reference solution. This copy is test-only.
	files["src/workflows/dispatch-cycle.before.js"] = files["src/workflows/dispatch-cycle.js"]
	for path, content := range references {
		if _, exists := files[path]; !exists || !strings.HasPrefix(path, "src/") {
			t.Fatalf("reference solution has no source target: %s", path)
		}
		files[path] = []byte(content)
	}
	root := t.TempDir()
	for path, data := range files {
		filename := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	checks, err := os.ReadFile("testdata/after-examples.mjs")
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(root, "after-examples.mjs")
	if err := os.WriteFile(filename, checks, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(node, "--test", filename)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("reference example checks failed: %v\n%s", err, output)
	}
}

func TestDispatchCycleRequiresAllRules(t *testing.T) {
	files, err := ProjectFiles()
	if err != nil {
		t.Fatal(err)
	}
	const path = "src/workflows/dispatch-cycle.js"
	before := string(files[path])
	after := readReferenceSolutions(t)[path]
	if lines := strings.Count(before, "\n"); lines < 400 {
		t.Fatalf("combined workflow should exercise a large file, got %d lines", lines)
	}
	if after == "" {
		t.Fatal("combined workflow has no test-only reference solution")
	}
	pack, err := Rules()
	if err != nil {
		t.Fatal(err)
	}
	// Match the executable body as well as the full file, so importing all names
	// cannot masquerade as ten actual migration scenarios.
	body := before[strings.Index(before, "// A dispatch cycle"):]
	for name, raw := range pack.Files {
		if !strings.HasSuffix(name, "/rule.json") {
			continue
		}
		rule, err := ruleformat.Decode(raw)
		if err != nil {
			t.Fatal(err)
		}
		if rule.Pattern != "" && !regexp.MustCompile(rule.Pattern).MatchString(body) {
			t.Errorf("%s has no applicable operation in the combined workflow body", name)
		}
	}
	for id, required := range map[string][2]string{
		"R001": {"var completedCycles = 0", "let completedCycles = 0"},
		"R002": {"settings.label == null ? 'Dispatch desk' : settings.label", "settings.label ?? 'Dispatch desk'"},
		"R003": {"from '../../lib/parcel-kit.js'", "from '../../lib/parcel-client.js'"},
	} {
		if !strings.Contains(before, required[0]) || !strings.Contains(after, required[1]) || strings.Contains(after, required[0]) {
			t.Errorf("%s must require an actual change in the combined workflow", id)
		}
	}
	for _, pattern := range strings.Split(strings.TrimSpace(string(pack.Files["patterns/legacy-symbols.txt"])), "\n") {
		if regexp.MustCompile(pattern).MatchString(after) {
			t.Errorf("reference solution leaves a retired SDK reference matching %s", pattern)
		}
	}
}

func readReferenceSolutions(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile("testdata/reference-solutions.json")
	if err != nil {
		t.Fatal(err)
	}
	var references map[string]string
	if err := json.Unmarshal(data, &references); err != nil {
		t.Fatal(err)
	}
	return references
}

// Exact worked solutions should never be supplied as demo instructions. Ignore
// whitespace so changing indentation or line breaks cannot hide a copied file.
func TestRulesDoNotContainTargetSolutions(t *testing.T) {
	files, err := ProjectFiles()
	if err != nil {
		t.Fatal(err)
	}
	pack, err := Rules()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := assets.ReadDir("testdata"); err == nil {
		t.Fatal("test-only reference solutions must not be embedded in the app")
	}
	normalize := func(code string) string { return strings.Join(strings.Fields(code), "") }
	fullSources := make(map[string]string)
	exportedNames := make(map[string]string)
	exports := regexp.MustCompile(`\bexport\s+(?:async\s+)?(?:function|class|const|let)\s+([A-Za-z_$][A-Za-z0-9_$]*)`)
	for path, source := range files {
		if !strings.HasPrefix(path, "src/") || !strings.HasSuffix(path, ".js") {
			continue
		}
		fullSources[normalize(string(source))] = path
		for _, match := range exports.FindAllSubmatch(source, -1) {
			exportedNames[string(match[1])] = path
		}
	}
	for path, source := range readReferenceSolutions(t) {
		fullSources[normalize(source)] = "test-only solution for " + path
	}
	for name, raw := range pack.Files {
		if !strings.HasSuffix(name, "/rule.json") {
			continue
		}
		rule, err := ruleformat.Decode(raw)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for section, example := range map[string]string{"before": rule.Before, "after": rule.After} {
			if target, exact := fullSources[normalize(example)]; exact {
				t.Errorf("%s %s reproduces the complete %s", name, section, target)
			}
			for identifier, path := range exportedNames {
				if regexp.MustCompile(`\b` + regexp.QuoteMeta(identifier) + `\b`).MatchString(example) {
					t.Errorf("%s %s uses target-specific exported name %s from %s", name, section, identifier, path)
				}
			}
		}
	}
}

func TestIndividualRulePatternsStillReachDemoScenarios(t *testing.T) {
	files, err := ProjectFiles()
	if err != nil {
		t.Fatal(err)
	}
	pack, err := Rules()
	if err != nil {
		t.Fatal(err)
	}
	// Preserve coverage of the intended workflows, transfer/composite examples,
	// and cases where the agent must decide to hold instead of blindly convert.
	scenarios := map[string][]string{
		"R101": {"src/orders/place-order.js", "src/shared/host-transaction.js"},
		"R102": {"src/ui/live-orders.js"},
		"R103": {"src/jobs/export-orders.js", "src/jobs/order-index.js", "src/jobs/dynamic-provider.js", "src/ui/external-list-hook.js"},
		"R104": {"src/billing/invoice-export.js", "src/billing/settlement-audit.js"},
		"R105": {"src/jobs/process-shipment.js"},
		"R106": {"src/billing/payment-request.js", "src/billing/settlement-audit.js"},
		"R107": {"src/jobs/reconcile-payments.js"},
		"R108": {"src/orders/bulk-reserve.js"},
		"R109": {"src/billing/settle-invoice.js"},
		"R110": {"src/shared/runtime-options.js", "src/billing/opaque-config.js"},
	}
	for id, paths := range scenarios {
		rule, err := ruleformat.Decode(pack.Files["rules/"+id+"/rule.json"])
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if strings.TrimSpace(rule.Pattern) == "" {
			t.Fatalf("%s must keep an individual applicability pattern", id)
		}
		pattern, err := regexp.Compile(rule.Pattern)
		if err != nil {
			t.Fatalf("%s pattern: %v", id, err)
		}
		for _, path := range paths {
			if !pattern.Match(files[path]) {
				t.Errorf("%s no longer reaches intended scenario %s", id, path)
			}
		}
	}
}

func TestDemoJavaScriptSyntax(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is not installed; JavaScript syntax checks are optional")
	}
	files, err := ProjectFiles()
	if err != nil {
		t.Fatal(err)
	}
	pack, err := Rules()
	if err != nil {
		t.Fatal(err)
	}
	scripts := make(map[string]string)
	for path, content := range files {
		if strings.HasSuffix(path, ".js") {
			scripts[path] = string(content)
		}
	}
	for path, raw := range pack.Files {
		if !strings.HasSuffix(path, "/rule.json") {
			continue
		}
		rule, err := ruleformat.Decode(raw)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		scripts[path+"/before"] = rule.Before
		scripts[path+"/after"] = rule.After
	}
	for path, script := range scripts {
		t.Run(path, func(t *testing.T) {
			command := exec.Command(node, "--input-type=module", "--check")
			command.Stdin = strings.NewReader(script)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("invalid demo JavaScript: %v\n%s", err, output)
			}
		})
	}
}
