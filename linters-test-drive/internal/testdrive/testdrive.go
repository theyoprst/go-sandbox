package testdrive

import (
	"context"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"go/token"
	"log"
	"math"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"text/template"

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
	// td.printReport(report)
	if err := td.renderReport(report); err != nil {
		log.Printf("Error rendering report: %v", err)
		return subcommands.ExitFailure
	}
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

type Report struct {
	TotalIssuesCount int
	Sections         map[string][]linterReport
	SectionsOrder    []string
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
	FullName   FullName
	Issues     []result.Issue
	SubLinters []linterReport
	Intersects []LinterShare

	subLintersMap  map[string]*linterReport
	intersectCount map[FullName]int
}

type LinterShare struct {
	Name  FullName
	Share float64
}

func (s LinterShare) Percent() int {
	return int(math.Round(s.Share * 100.0))
}

func (td *Cmd) buildReport(result printers.JSONResult, linterToSection map[string]string) Report {
	r := Report{
		Sections:      map[string][]linterReport{},
		SectionsOrder: sectionsOrder,
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
				FullName:       FullName{name: name},
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
					FullName:       fullName,
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
			linterInfo.Intersects = append(linterInfo.Intersects, LinterShare{
				Name:  fullName,
				Share: float64(count) / float64(n),
			})
		}
		sort.Slice(linterInfo.Intersects, func(i, j int) bool {
			return linterInfo.Intersects[i].Share > linterInfo.Intersects[j].Share
		})
		linterInfo.Intersects = filterIntersections(linterInfo.Intersects, 0.5)
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

func filterIntersections(linters []LinterShare, shareThreshold float64) []LinterShare {
	i := 0
	for ; i < len(linters) && linters[i].Share > shareThreshold; i++ {
	}
	return linters[:i]
}

func formatIntersections(linters []LinterShare, shareThreshold float64) string {
	i := 0
	for ; i < len(linters) && linters[i].Share > shareThreshold; i++ {
	}
	var parts []string
	for j := 0; j < i; j++ {
		parts = append(parts, fmt.Sprintf("%s (%0.0f%%)", linters[j].Name, 100*linters[j].Share))
	}
	if len(parts) == 0 {
		return ""
	}
	return "; intersects with " + strings.Join(parts, ", ")
}

//go:embed report-template.md
var reportTemplate string

func (td *Cmd) renderReport(r Report) error {
	tmpl, err := template.New("template").Funcs(template.FuncMap{
		"underLinePointer": UnderLinePointer,
		"formatText":       FormatText,
	}).Parse(reportTemplate)
	if err != nil {
		return err
	}
	return tmpl.Execute(os.Stdout, r)
}

func (td *Cmd) printReport(r Report) {
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
			for _, issue := range linter.Issues {
				_ = issue
				// issue.SourceLines
			}
		}
	}
}

func trimLeftCommonSpaces(issue *result.Issue) {
	if len(issue.SourceLines) == 0 {
		return
	}
	for {
		if len(issue.SourceLines[0]) == 0 {
			break
		}
		first := issue.SourceLines[0][0]
		if first != ' ' && first != '\t' {
			break
		}
		isCommon := true
		for i := 1; i < len(issue.SourceLines); i++ {
			if len(issue.SourceLines[i]) == 0 || issue.SourceLines[i][0] != first {
				isCommon = false
				break
			}
		}
		if !isCommon {
			break
		}
		for i := range issue.SourceLines {
			issue.SourceLines[i] = issue.SourceLines[i][1:]
		}
		issue.Pos.Column--
	}
}

func FormatText(i *result.Issue) string {
	trimLeftCommonSpaces(i)
	return strings.Join(append(i.SourceLines, UnderLinePointer(i)), "\n")
}

func UnderLinePointer(i *result.Issue) string {
	// if column == 0 it means column is unknown (e.g. for gosec)
	if len(i.SourceLines) != 1 || i.Pos.Column == 0 {
		return ""
	}

	col0 := i.Pos.Column - 1
	line := i.SourceLines[0]
	prefixRunes := make([]rune, 0, len(line))
	for j := 0; j < len(line) && j < col0; j++ {
		if line[j] == '\t' {
			prefixRunes = append(prefixRunes, '\t')
		} else {
			prefixRunes = append(prefixRunes, ' ')
		}
	}

	return string(prefixRunes) + "^"
}

var subLinterRe = regexp.MustCompile(`^([\w-]+(\([\w\s-]+\))?):`)

func parseSubLinter(text string) string {
	matches := subLinterRe.FindStringSubmatch(text)
	if len(matches) < 2 {
		return ""
	}
	return matches[1]
}
