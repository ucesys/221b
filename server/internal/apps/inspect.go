package apps

import (
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/go-git/go-billy/v5/util"
	"sigs.k8s.io/yaml"
)

// ChartCandidate is a chart found in a repository (M-07).
type ChartCandidate struct {
	Path        string `json:"path"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	AppVersion  string `json:"appVersion,omitempty"`
	Description string `json:"description,omitempty"`
	// ValuesFiles are the other YAML files in the chart's directory and
	// its first subdirectories (values-prod.yaml, values/eu.yaml), the
	// ones a deployment may lay over values.yaml (R2-08).
	ValuesFiles []string `json:"valuesFiles,omitempty"`
}

// valuesFiles lists the YAML files beside a chart's values.yaml, and
// those one directory down, templates, charts, crds and ci left out.
func valuesFiles(c *Checkout, root string) []string {
	var out []string
	add := func(dir, name string) {
		if n := strings.ToLower(name); (strings.HasSuffix(n, ".yaml") || strings.HasSuffix(n, ".yml")) && n != "chart.yaml" && n != "chart.lock" && n != "values.yaml" {
			out = append(out, path.Join(dir, name))
		}
	}
	entries, err := c.FS.ReadDir(root)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		n := e.Name()
		switch {
		case strings.HasPrefix(n, "."):
		case !e.IsDir():
			add("", n)
		case n == "templates" || n == "charts" || n == "crds" || n == "ci":
		default:
			sub, err := c.FS.ReadDir(path.Join(root, n))
			if err != nil {
				continue
			}
			for _, f := range sub {
				if !f.IsDir() {
					add(n, f.Name())
				}
			}
		}
	}
	sort.Strings(out)
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

// ReadmeHints is what a README's helm install example says (M-07): the
// release name, the namespace, the parameters and the values file it
// names, with the file's content when it is in the repository.
type ReadmeHints struct {
	File            string   `json:"file"`
	Chart           string   `json:"chart,omitempty"`
	Name            string   `json:"name,omitempty"`
	Namespace       string   `json:"namespace,omitempty"`
	CreateNamespace bool     `json:"createNamespace,omitempty"`
	Params          []string `json:"params,omitempty"`
	ValuesFile      string   `json:"valuesFile,omitempty"`
	Values          string   `json:"values,omitempty"`
}

// Inspection is what a repository offers someone adding an application:
// its charts, best first, and the README's hints.
type Inspection struct {
	Charts []ChartCandidate `json:"charts"`
	Readme *ReadmeHints     `json:"readme,omitempty"`
}

const (
	inspectDepth    = 4
	readmeMax       = 256 << 10
	valuesFileMax   = 64 << 10
	chartCandidates = 20
)

// Inspect looks over a checkout for the charts in it and the README's
// install example (M-07). It reads files; it runs nothing.
func Inspect(c *Checkout) Inspection {
	var out Inspection
	findCharts(c, "", 0, &out.Charts)
	readme := readmeAt(c, "")
	if readme == "" && len(out.Charts) > 0 {
		readme = readmeAt(c, out.Charts[0].Path)
	}
	if readme != "" {
		out.Readme = readmeHints(c, readme, out.Charts)
	}
	// The chart the README installs comes first; then the shallowest path.
	sort.SliceStable(out.Charts, func(i, j int) bool {
		a, b := out.Charts[i], out.Charts[j]
		if out.Readme != nil && out.Readme.Chart != "" {
			if (a.Path == out.Readme.Chart) != (b.Path == out.Readme.Chart) {
				return a.Path == out.Readme.Chart
			}
		}
		da, db := strings.Count(a.Path, "/"), strings.Count(b.Path, "/")
		if da != db {
			return da < db
		}
		return a.Path < b.Path
	})
	return out
}

// findCharts walks the checkout for Chart.yaml files, a few levels deep,
// leaving out dot directories, vendored modules and a chart's own charts/.
func findCharts(c *Checkout, dir string, depth int, out *[]ChartCandidate) {
	if depth > inspectDepth || len(*out) >= chartCandidates {
		return
	}
	root := "/" + dir
	if b, err := util.ReadFile(c.FS, path.Join(root, "Chart.yaml")); err == nil {
		var m struct {
			Name        string `json:"name"`
			Version     string `json:"version"`
			AppVersion  string `json:"appVersion"`
			Description string `json:"description"`
		}
		if yaml.Unmarshal(b, &m) == nil && m.Name != "" {
			*out = append(*out, ChartCandidate{Path: dir, Name: m.Name, Version: m.Version, AppVersion: m.AppVersion, Description: m.Description, ValuesFiles: valuesFiles(c, root)})
		}
		// A chart's charts/ holds its dependencies, not applications.
		entries, err := c.FS.ReadDir(root)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() && e.Name() != "charts" && !strings.HasPrefix(e.Name(), ".") {
				findCharts(c, path.Join(dir, e.Name()), depth+1, out)
			}
		}
		return
	}
	entries, err := c.FS.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() || strings.HasPrefix(n, ".") || n == "node_modules" || n == "vendor" {
			continue
		}
		findCharts(c, path.Join(dir, n), depth+1, out)
	}
}

// readmeAt is the README in the directory, or "" without one.
func readmeAt(c *Checkout, dir string) string {
	for _, n := range []string{"README.md", "readme.md", "README", "README.txt"} {
		p := path.Join("/"+dir, n)
		if fi, err := c.FS.Stat(p); err == nil && !fi.IsDir() && fi.Size() <= readmeMax {
			return strings.TrimPrefix(p, "/")
		}
	}
	return ""
}

var fenceRe = regexp.MustCompile("(?s)```[a-zA-Z]*\n(.*?)```")

// readmeHints reads the README's first helm install (or upgrade --install)
// example: the release name, the chart path, the namespace, --set
// parameters and the values file; with no values file, the first fenced
// YAML block whose keys the chart's values.yaml has.
func readmeHints(c *Checkout, file string, charts []ChartCandidate) *ReadmeHints {
	b, err := util.ReadFile(c.FS, "/"+file)
	if err != nil {
		return nil
	}
	text := string(b)
	h := &ReadmeHints{File: file}
	// Lines continued with a backslash are one command.
	joined := strings.ReplaceAll(text, "\\\n", " ")
	for _, line := range strings.Split(joined, "\n") {
		line = strings.TrimSpace(line)
		i := strings.Index(line, "helm ")
		if i < 0 {
			continue
		}
		args := strings.Fields(line[i+5:])
		if len(args) == 0 || (args[0] != "install" && args[0] != "upgrade") {
			continue
		}
		if parseHelmInstall(args[1:], h) {
			break
		}
	}
	if h.ValuesFile != "" {
		for _, base := range []string{"", h.Chart} {
			p := path.Join("/"+base, h.ValuesFile)
			if fi, err := c.FS.Stat(p); err == nil && !fi.IsDir() && fi.Size() <= valuesFileMax {
				if vb, err := util.ReadFile(c.FS, p); err == nil && yaml.Unmarshal(vb, &map[string]any{}) == nil {
					h.Values = string(vb)
					break
				}
			}
		}
	}
	if h.Values == "" {
		h.Values = valuesBlock(c, text, h.Chart, charts)
	}
	if h.Name == "" && h.Namespace == "" && h.Chart == "" && len(h.Params) == 0 && h.Values == "" {
		return nil
	}
	return h
}

// parseHelmInstall reads the arguments after "helm install": the release
// name, the chart, and the flags that matter. It is false when the line
// is not an install (a chart from a repository is still one).
func parseHelmInstall(args []string, h *ReadmeHints) bool {
	var positional []string
	flagValue := func(i *int) string {
		a := args[*i]
		if eq := strings.IndexByte(a, '='); eq >= 0 {
			return strings.Trim(a[eq+1:], `"'`)
		}
		if *i+1 < len(args) {
			*i++
			return strings.Trim(args[*i], `"'`)
		}
		return ""
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--install" || a == "-i" || a == "--wait" || a == "--atomic" || a == "--dry-run" || a == "--debug":
		case a == "--create-namespace":
			h.CreateNamespace = true
		case a == "--generate-name" || a == "-g":
			positional = append(positional, "")
		case a == "-n" || a == "--namespace" || strings.HasPrefix(a, "--namespace="):
			h.Namespace = flagValue(&i)
		case a == "-f" || a == "--values" || strings.HasPrefix(a, "--values="):
			h.ValuesFile = flagValue(&i)
		case a == "--set" || a == "--set-string" || strings.HasPrefix(a, "--set=") || strings.HasPrefix(a, "--set-string="):
			for _, p := range strings.Split(flagValue(&i), ",") {
				if p = strings.TrimSpace(p); strings.Contains(p, "=") {
					h.Params = append(h.Params, p)
				}
			}
		case strings.HasPrefix(a, "-"):
			// Another flag with a value: skip it when it has no =.
			if !strings.Contains(a, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
			}
		default:
			positional = append(positional, strings.Trim(a, `"'`))
		}
	}
	if len(positional) == 0 {
		return false
	}
	if n := positional[0]; n != "" && !strings.ContainsAny(n, "<>${}") && len(n) <= 53 {
		h.Name = n
	}
	if len(positional) > 1 {
		chart := strings.Trim(positional[1], "/")
		if strings.HasPrefix(chart, "./") || strings.HasPrefix(chart, "../") || !strings.Contains(chart, "/") || strings.Count(chart, "/") > 0 && !strings.Contains(chart, "://") {
			chart = strings.TrimPrefix(chart, "./")
			if chart == "." {
				chart = ""
			}
			h.Chart = chart
		}
	}
	// A placeholder name is no name.
	if strings.EqualFold(h.Name, "release-name") || strings.EqualFold(h.Name, "my-release") || strings.EqualFold(h.Name, "name") {
		h.Name = ""
	}
	if strings.ContainsAny(h.Namespace, "<>${}") {
		h.Namespace = ""
	}
	return true
}

// valuesBlock is the first fenced YAML block whose top-level keys the
// chart's values.yaml all has, as a values file to start from.
func valuesBlock(c *Checkout, readme, chartDir string, charts []ChartCandidate) string {
	dir := chartDir
	if dir == "" && len(charts) > 0 {
		dir = charts[0].Path
	}
	vb, err := util.ReadFile(c.FS, path.Join("/"+dir, "values.yaml"))
	if err != nil {
		return ""
	}
	var known map[string]any
	if yaml.Unmarshal(vb, &known) != nil || len(known) == 0 {
		return ""
	}
	for _, m := range fenceRe.FindAllStringSubmatch(readme, -1) {
		block := m[1]
		var got map[string]any
		if yaml.Unmarshal([]byte(block), &got) != nil || len(got) == 0 {
			continue
		}
		ok := true
		for k := range got {
			if _, has := known[k]; !has || k == "apiVersion" || k == "kind" {
				ok = false
				break
			}
		}
		if ok {
			return strings.TrimRight(block, "\n") + "\n"
		}
	}
	return ""
}
