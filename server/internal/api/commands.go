package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/watson"
)

// An application's own deploy commands (asked October 30, 2026): named,
// each a redeploy from the source with its own --set parameters over the
// saved values and parameters, kept with the application and offered in
// its Tools menu. Saving and deleting are the console's own records;
// running one is a Watson card, like Redeploy.

// commandInput is what the page sends to add or change a command.
type commandInput struct {
	// Name renames the command; empty keeps the name in the path.
	Name   string   `json:"name,omitempty"`
	Note   string   `json:"note,omitempty"`
	Params []string `json:"params"`
}

// checkCommandParams validates the parameters as readApp does, and keeps
// out what the Watson line could not carry in single quotes.
func checkCommandParams(params []string) ([]string, error) {
	out := make([]string, 0, len(params))
	for i, p := range params {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if k, _, ok := strings.Cut(p, "="); !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("parameter %d is not key=value", i+1)
		}
		if strings.ContainsAny(p, "'$`\n") {
			return nil, fmt.Errorf("parameter %d: single quotes, $ and backquotes are not allowed", i+1)
		}
		out = append(out, p)
	}
	if _, err := apps.Values("", out); err != nil {
		return nil, err
	}
	return out, nil
}

// setFlags turns the parameters into --set flags of the Watson line.
func setFlags(params []string) []string {
	out := make([]string, 0, 2*len(params))
	for _, p := range params {
		out = append(out, "--set", "'"+p+"'")
	}
	return out
}

// appCommand reads the application for a command request.
func (s *Server) appCommand(w http.ResponseWriter, r *http.Request) (c watson.Cluster, who watson.Who, rec apps.Record, ok bool) {
	c, who, ok = s.appReady(w, r)
	if !ok || !s.mayManageApps(w, r) {
		return c, who, rec, false
	}
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	if !appNames(w, ns, name) {
		return c, who, rec, false
	}
	rec, _, err := s.opts.Apps.Get(r.Context(), c.ID, ns, name)
	if errors.Is(err, apps.ErrNotFound) {
		jsonError(w, http.StatusNotFound, "the console keeps no source for this release: set one first")
		return c, who, rec, false
	}
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return c, who, rec, false
	}
	return c, who, rec, true
}

// putAppCommand adds a command, or changes the one named in the path.
func (s *Server) putAppCommand(w http.ResponseWriter, r *http.Request) {
	_, who, rec, ok := s.appCommand(w, r)
	if !ok {
		return
	}
	var in commandInput
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		jsonError(w, http.StatusBadRequest, "the command is not valid: "+err.Error())
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = r.PathValue("command")
	}
	if err := apps.CheckCommandName(name); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	params, err := checkCommandParams(in.Params)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(params) == 0 {
		jsonError(w, http.StatusBadRequest, "a command has at least one parameter; without any it is Redeploy")
		return
	}
	i := rec.CommandIndex(r.PathValue("command"))
	if j := rec.CommandIndex(name); j >= 0 && j != i {
		jsonError(w, http.StatusConflict, fmt.Sprintf("the application already has a command called %q", rec.Commands[j].Name))
		return
	}
	now := s.opts.Now().UTC()
	note := strings.TrimSpace(in.Note)
	if len([]rune(note)) > apps.MaxCommandNote {
		jsonError(w, http.StatusBadRequest, fmt.Sprintf("a note is at most %d characters, so it fits the menu", apps.MaxCommandNote))
		return
	}
	if i < 0 {
		if len(rec.Commands) >= apps.MaxCommands {
			jsonError(w, http.StatusBadRequest, fmt.Sprintf("an application has at most %d commands", apps.MaxCommands))
			return
		}
		rec.Commands = append(rec.Commands, apps.Command{Name: name, Note: note, Params: params, Created: now, CreatedBy: who.User, Updated: now, UpdatedBy: who.User})
	} else {
		c := &rec.Commands[i]
		c.Name, c.Note, c.Params, c.Updated, c.UpdatedBy = name, note, params, now, who.User
	}
	rec.Updated, rec.UpdatedBy = now, who.User
	if err := s.opts.Apps.Save(r.Context(), rec, nil); err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.record(r, ActionAppCommand, rec.Namespace+"/"+rec.Name, "command="+quoteDetail(name)+" params="+strconv.Itoa(len(params)))
	b, _ := json.Marshal(appView{Record: rec})
	writeJSON(w, r, b)
}

// deleteAppCommand removes a command.
func (s *Server) deleteAppCommand(w http.ResponseWriter, r *http.Request) {
	_, who, rec, ok := s.appCommand(w, r)
	if !ok {
		return
	}
	i := rec.CommandIndex(r.PathValue("command"))
	if i < 0 {
		jsonError(w, http.StatusNotFound, "the application has no such command")
		return
	}
	name := rec.Commands[i].Name
	rec.Commands = append(rec.Commands[:i], rec.Commands[i+1:]...)
	rec.Updated, rec.UpdatedBy = s.opts.Now().UTC(), who.User
	if err := s.opts.Apps.Save(r.Context(), rec, nil); err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.record(r, ActionAppCommand, rec.Namespace+"/"+rec.Name, "command="+quoteDetail(name)+" deleted")
	b, _ := json.Marshal(appView{Record: rec})
	writeJSON(w, r, b)
}

// postAppCommandRun redeploys the application with the command's
// parameters: a Watson card, as Redeploy is. ?install=1 adds --install
// for a release that is not there.
func (s *Server) postAppCommandRun(w http.ResponseWriter, r *http.Request) {
	c, who, rec, ok := s.appCommand(w, r)
	if !ok {
		return
	}
	i := rec.CommandIndex(r.PathValue("command"))
	if i < 0 {
		jsonError(w, http.StatusNotFound, "the application has no such command")
		return
	}
	extra := append(installFlags(rec, r.URL.Query().Get("install") == "1"), setFlags(rec.Commands[i].Params)...)
	s.submitApp(w, r, c, who, appLine("upgrade", rec.Namespace, rec.Name, rec.Source, extra...))
}
