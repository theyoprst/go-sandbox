package testdrive

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"go/token"
	"log"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"

	"github.com/golangci/golangci-lint/pkg/lint/linter"
	"github.com/golangci/golangci-lint/pkg/lint/lintersdb"
	"github.com/golangci/golangci-lint/pkg/printers"
	"github.com/golangci/golangci-lint/pkg/result"
	"github.com/google/subcommands"
	"golang.org/x/exp/slices"
)

type Cmd struct {
	sourcesPath string
}

func (*Cmd) Name() string {
	return "run"
}

func (*Cmd) Synopsis() string {
	return "run golangci-lint with all the linters and build a report"
}

func (*Cmd) Usage() string {
	return `linters-test-drive`
}

func (td *Cmd) SetFlags(f *flag.FlagSet) {
	f.StringVar(&td.sourcesPath, "sources-path", "", "path to go sources")
}

var sectionsOrder = []string{
	"default", "bugs", "unused", "format", "complexity", "performance", "test", "comment", "style", "other",
}

func Section(lntr *linter.Config) string {
	if lntr.EnabledByDefault {
		return "default"
	}
	for _, section := range sectionsOrder {
		if slices.Contains(lntr.InPresets, section) {
			return section
		}
	}
	return "other"
}

func (td *Cmd) Execute(_ context.Context, _ *flag.FlagSet, _ ...any) subcommands.ExitStatus {
	allLinters := lintersdb.NewManager(nil, nil).GetAllSupportedLinterConfigs()
	linterToSection := map[string]string{}
	for _, linter := range allLinters {
		if linter.Deprecation != nil {
			continue
		}
		linterToSection[linter.Name()] = Section(linter)
	}
	jsonResult, err := td.callGolangcilint()
	if err != nil {
		log.Printf("`golangci-lint run` call error %s", err)
		return subcommands.ExitFailure
	}
	report := td.buildReport(jsonResult, linterToSection)
	td.printReport(report)
	return subcommands.ExitSuccess
}

func (td *Cmd) callGolangcilint() (printers.JSONResult, error) {
	cmd := exec.Command("golangci-lint", "run", "--out-format=json", "--config=.golangci-testdrive.yml")
	cmd.Dir = td.sourcesPath
	cmd.Stderr = os.Stderr
	var jsonResult printers.JSONResult
	output, err := cmd.Output()
	if cmd.ProcessState.ExitCode() != 1 && cmd.ProcessState.ExitCode() != 0 {
		return jsonResult, fmt.Errorf("unexpected status code %d: %s", cmd.ProcessState.ExitCode(), err)
	}
	if err := json.Unmarshal(output, &jsonResult); err != nil {
		return jsonResult, fmt.Errorf("cannot unmarshal golangci-lint json output: %s", err)
	}
	return jsonResult, nil
}

type report struct {
	TotalIssuesCount int
	Sections         map[string][]linterReport
}

type FullName struct {
	name    string
	subName string
}

func (fn FullName) String() string {
	if fn.subName == "" {
		return fn.name
	}
	return fn.name + "/" + fn.subName
}

type linterReport struct {
	Name       string
	Issues     []result.Issue
	SubLinters []linterReport
	Intersects []linterShare

	subLintersMap  map[string]*linterReport
	intersectCount map[FullName]int
}

type linterShare struct {
	name  FullName
	share float64
}

func (td *Cmd) buildReport(result printers.JSONResult, linterToSection map[string]string) report {
	r := report{
		Sections: map[string][]linterReport{},
	}
	linterInfos := make(map[string]*linterReport)
	allLinterInfos := make(map[FullName]*linterReport) // including sublinters
	lintersPerPosition := make(map[token.Position]map[FullName]struct{})
	for _, issue := range result.Issues {
		name := issue.FromLinter
		if linterToSection[name] == "" {
			// Skip deprecated linters
			continue
		}
		r.TotalIssuesCount++
		subName := parseSubLinter(issue.Text)
		if lintersPerPosition[issue.Pos] == nil {
			lintersPerPosition[issue.Pos] = make(map[FullName]struct{})
		}
		fullName := FullName{name: name, subName: subName}
		lintersPerPosition[issue.Pos][fullName] = struct{}{}
		if linterInfos[name] == nil {
			linterInfos[name] = &linterReport{
				Name:           name,
				subLintersMap:  make(map[string]*linterReport),
				intersectCount: make(map[FullName]int),
			}
		}
		linterInfo := linterInfos[name]
		allLinterInfos[fullName] = linterInfo
		linterInfo.Issues = append(linterInfo.Issues, issue)
		if subName != "" {
			if linterInfo.subLintersMap[subName] == nil {
				linterInfo.subLintersMap[subName] = &linterReport{
					Name:           subName,
					intersectCount: make(map[FullName]int),
				}
			}
			subLinterInfo := linterInfo.subLintersMap[subName]
			allLinterInfos[fullName] = subLinterInfo
			subLinterInfo.Issues = append(subLinterInfo.Issues, issue)
		}
	}
	for _, lintersSet := range lintersPerPosition {
		for fullName1 := range lintersSet {
			for fullName2 := range lintersSet {
				if fullName1 != fullName2 {
					allLinterInfos[fullName1].intersectCount[fullName2]++
				}
			}
		}
	}
	for _, linterInfo := range allLinterInfos {
		n := len(linterInfo.Issues)
		for fullName, count := range linterInfo.intersectCount {
			linterInfo.Intersects = append(linterInfo.Intersects, linterShare{
				name:  fullName,
				share: float64(count) / float64(n),
			})
		}
		sort.Slice(linterInfo.Intersects, func(i, j int) bool {
			return linterInfo.Intersects[i].share > linterInfo.Intersects[j].share
		})
	}
	for _, linterInfo := range linterInfos {
		section := linterToSection[linterInfo.Name]
		for _, subLinterInfo := range linterInfo.subLintersMap {
			linterInfo.SubLinters = append(linterInfo.SubLinters, *subLinterInfo)
		}
		sort.Slice(linterInfo.SubLinters, func(i, j int) bool {
			return linterInfo.SubLinters[i].Name < linterInfo.SubLinters[j].Name
		})
		r.Sections[section] = append(r.Sections[section], *linterInfo)
	}
	for section := range r.Sections {
		sort.Slice(r.Sections[section], func(i, j int) bool {
			return r.Sections[section][i].Name < r.Sections[section][j].Name
		})
	}
	return r
}

func formatIntersections(linters []linterShare, shareThreshold float64) string {
	i := 0
	for ; i < len(linters) && linters[i].share > shareThreshold; i++ {
	}
	var parts []string
	for j := 0; j < i; j++ {
		parts = append(parts, fmt.Sprintf("%s (%0.0f%%)", linters[j].name, 100*linters[j].share))
	}
	if len(parts) == 0 {
		return ""
	}
	return "; intersects with " + strings.Join(parts, ", ")
}

func (td *Cmd) printReport(r report) {
	const intersectionThreshold = 0.5
	log.Printf("There are %d issues found", r.TotalIssuesCount)
	for _, section := range sectionsOrder {
		log.Printf("=== Section %s ===", section)
		for _, linter := range r.Sections[section] {
			log.Printf("  * %s: %d issues%s",
				linter.Name, len(linter.Issues), formatIntersections(linter.Intersects, intersectionThreshold))
			for _, subLinter := range linter.SubLinters {
				log.Printf("    * %s: %d issues%s",
					subLinter.Name, len(subLinter.Issues), formatIntersections(subLinter.Intersects, intersectionThreshold))
			}
		}
	}
}

var subLinterRe = regexp.MustCompile(`^([\w-]+(\([\w\s-]+\))?):`)

func parseSubLinter(text string) string {
	matches := subLinterRe.FindStringSubmatch(text)
	if len(matches) < 2 {
		return ""
	}
	return matches[1]
}
