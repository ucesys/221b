package sherlock

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// tabsFixture is what each tab accepts, as testdata/ui_tabs.json gives it
// and the browser's TAB_SPECS must equal (S-3).
type tabsFixture struct {
	Tabs map[string]struct {
		Filters map[string]struct {
			Type   string   `json:"type"`
			Values []string `json:"values"`
		} `json:"filters"`
		Path []struct {
			Name   string   `json:"name"`
			Values []string `json:"values"`
		} `json:"path"`
	} `json:"tabs"`
}

// The descriptions of view_open and view_filter name every tab, path,
// filter and value the browser accepts, from one fixture that the
// browser's own test holds TAB_SPECS to (S-3).
func TestViewToolsDescribeWhatTheBrowserAccepts(t *testing.T) {
	b, err := os.ReadFile("testdata/ui_tabs.json")
	if err != nil {
		t.Fatal(err)
	}
	var f tabsFixture
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	open, _ := uiAction("view_open")
	filter, _ := uiAction("view_filter")
	var schema struct {
		Properties struct {
			Tab struct {
				Enum []string `json:"enum"`
			} `json:"tab"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(open.InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	var tabs []string
	for id := range f.Tabs {
		tabs = append(tabs, id)
	}
	slices.Sort(tabs)
	enum := slices.Sorted(slices.Values(schema.Properties.Tab.Enum))
	if !slices.Equal(tabs, enum) {
		t.Errorf("view_open takes the tabs %v; the browser has %v", enum, tabs)
	}
	label := func(id string) string { return strings.ToUpper(id[:1]) + id[1:] + ": " }
	for _, id := range tabs {
		tab := f.Tabs[id]
		if !word(id).MatchString(open.Description) {
			t.Errorf("view_open does not name the tab %s", id)
		}
		if len(tab.Path) > 0 {
			var names []string
			for _, p := range tab.Path {
				names = append(names, p.Name)
			}
			head := id + " (path: [" + strings.Join(names, ", ") + "]"
			i := strings.Index(open.Description, head)
			if i < 0 {
				t.Errorf("view_open does not give the path of %s as %q", id, head)
				continue
			}
			said := group(open.Description[i+len(id)+1:])
			for _, p := range tab.Path {
				for _, v := range p.Values {
					if !word(v).MatchString(said) {
						t.Errorf("view_open does not say that %s's %s may be %s", id, p.Name, v)
					}
				}
			}
		}
		if len(tab.Filters) == 0 {
			continue
		}
		start := strings.Index(filter.Description, label(id))
		if start < 0 {
			t.Errorf("view_filter does not describe the filters of %s", id)
			continue
		}
		seg := filter.Description[start+len(label(id)):]
		end := strings.Index(seg, "Search text")
		for _, other := range tabs {
			if j := strings.Index(seg, label(other)); j >= 0 && (end < 0 || j < end) {
				end = j
			}
		}
		if end >= 0 {
			seg = seg[:end]
		}
		for name, spec := range tab.Filters {
			if !word(name).MatchString(seg) {
				t.Errorf("view_filter does not name %s's filter %s", id, name)
				continue
			}
			// An enum's values are checked where the description lists
			// them, after its name in parentheses.
			m := regexp.MustCompile(`(^|[ ,])` + regexp.QuoteMeta(name) + ` \(`).FindStringIndex(seg)
			if spec.Type != "enum" || m == nil {
				continue
			}
			said := group(seg[m[1]-1:])
			for _, v := range spec.Values {
				if !word(v).MatchString(said) {
					t.Errorf("view_filter does not say that %s's %s may be %s", id, name, v)
				}
			}
		}
	}
}

// word matches a name as a whole word of a description.
func word(s string) *regexp.Regexp {
	return regexp.MustCompile(`(^|[^\w-])` + regexp.QuoteMeta(s) + `($|[^\w-])`)
}

// group is the text of the parentheses s starts with.
func group(s string) string {
	depth := 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[1:i]
			}
		}
	}
	return s
}

// The steps under an answer read as words, never as a tool's or an
// agent's id (S-6).
func TestStepsNameToolsAndAgentsInWords(t *testing.T) {
	for _, a := range uiActions {
		if uiLabel(a.Name) == a.Name {
			t.Errorf("the step of %s shows its id", a.Name)
		}
	}
	if got := uiLabel("open_tool_dialog"); got != "Opened a tool dialog" {
		t.Errorf("open_tool_dialog: %q", got)
	}
	sh := &Agent{ID: SherlockID, Roster: []RosterEntry{{ID: "changePlanner", Name: "Change planner"}, {ID: "mycroft", Name: "Mycroft"}}}
	for id, want := range map[string]string{"changePlanner": "Asked the Change planner", "mycroft": "Asked Mycroft", "gone": "Asked gone"} {
		if got := askedLabel(sh, id); got != want {
			t.Errorf("%s: %q, want %q", id, got, want)
		}
	}
}

// The prompt asks for fewer view changes than the hard cap allows, and
// says the number the code keeps (S-7).
func TestThePromptsViewChangesComeFromTheConstant(t *testing.T) {
	if ViewChangesAsked >= MaxUIActions {
		t.Fatalf("the prompt asks for %d view changes; the cap is %d", ViewChangesAsked, MaxUIActions)
	}
	if !strings.Contains(BuiltIn(true, ""), fmt.Sprintf("Make at most %d changes for one question.", ViewChangesAsked)) {
		t.Error("the prompt does not say the constant's number")
	}
}

// view_admin's page list is the browser's, from one fixture its own test
// holds ADMIN_VIEW_PAGES to, and its description names every page, the
// pages that open an item's drawer and those with a search box (SR-12).
func TestViewAdminDescribesThePagesTheBrowserOpens(t *testing.T) {
	b, err := os.ReadFile("testdata/ui_admin.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Pages map[string]struct {
			Item   bool `json:"item"`
			Search bool `json:"search"`
		} `json:"pages"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	a, ok := uiAction("view_admin")
	if !ok || !a.Changes {
		t.Fatal("no view_admin that changes the view")
	}
	var schema struct {
		Properties struct {
			Page struct {
				Enum []string `json:"enum"`
			} `json:"page"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(a.InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	var pages []string
	for id := range f.Pages {
		pages = append(pages, id)
	}
	slices.Sort(pages)
	if enum := slices.Sorted(slices.Values(schema.Properties.Page.Enum)); !slices.Equal(pages, enum) {
		t.Errorf("view_admin takes the pages %v; the browser opens %v", enum, pages)
	}
	i := strings.Index(a.Description, "search sets the search box of ")
	if i < 0 {
		t.Fatal("view_admin does not say which pages take a search")
	}
	searchable := a.Description[i : i+strings.Index(a.Description[i:], ".")]
	for _, id := range pages {
		if !word(id).MatchString(a.Description) {
			t.Errorf("view_admin does not name the page %s", id)
		}
		if f.Pages[id].Item != strings.Contains(a.Description, id+" (item: ") {
			t.Errorf("view_admin's description and the browser differ on whether %s takes an item", id)
		}
		if f.Pages[id].Search != word(id).MatchString(searchable) {
			t.Errorf("view_admin's description and the browser differ on whether %s takes a search", id)
		}
	}
}
