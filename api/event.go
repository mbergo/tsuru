// Copyright 2016 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/ajg/form"
	"github.com/tsuru/tsuru/app"
	"github.com/tsuru/tsuru/auth"
	"github.com/tsuru/tsuru/errors"
	"github.com/tsuru/tsuru/event"
	"github.com/tsuru/tsuru/permission"
	"github.com/tsuru/tsuru/provision"
	"gopkg.in/mgo.v2/bson"
)

var evtPermMap = map[event.TargetType]evtPermChecker{
	event.TargetTypeApp:             &appPermChecker{},
	event.TargetTypeTeam:            &teamPermChecker{},
	event.TargetTypeService:         &servicePermChecker{},
	event.TargetTypeServiceInstance: &serviceInstancePermChecker{},
	event.TargetTypePool:            &poolPermChecker{},
	event.TargetTypeUser:            &userPermChecker{},
	event.TargetTypeContainer:       &containerPermChecker{},
	event.TargetTypeNode:            &nodePermChecker{},
	event.TargetTypeIaas:            &iaasPermChecker{},
	event.TargetTypeRole:            &rolePermChecker{},
}

type checkKind string

var (
	readCheckKind   checkKind = "read"
	updateCheckKind checkKind = "update"
)

type evtPermChecker interface {
	filter(t auth.Token) (*event.TargetFilter, error)
	check(t auth.Token, r *http.Request, e *event.Event, kind checkKind) (bool, error)
}

type appPermChecker struct{}

func (c *appPermChecker) filter(t auth.Token) (*event.TargetFilter, error) {
	contexts := permission.ContextsForPermission(t, permission.PermAppReadEvents)
	if len(contexts) == 0 {
		return nil, nil
	}
	apps, err := app.List(appFilterByContext(contexts, nil))
	if err != nil {
		return nil, err
	}
	if len(apps) == 0 {
		return nil, nil
	}
	allowed := event.TargetFilter{Type: event.TargetTypeApp}
	for _, a := range apps {
		allowed.Values = append(allowed.Values, a.Name)
	}
	return &allowed, nil
}

func (c *appPermChecker) check(t auth.Token, r *http.Request, e *event.Event, kind checkKind) (bool, error) {
	a, err := getAppFromContext(e.Target.Value, r)
	if err != nil {
		return false, err
	}
	perms := map[checkKind]*permission.PermissionScheme{
		readCheckKind:   permission.PermAppReadEvents,
		updateCheckKind: permission.PermAppUpdateEvents,
	}
	hasPermission := permission.Check(t, perms[kind],
		append(permission.Contexts(permission.CtxTeam, a.Teams),
			permission.Context(permission.CtxApp, a.Name),
			permission.Context(permission.CtxPool, a.Pool),
		)...,
	)
	return hasPermission, nil
}

type teamPermChecker struct{}

func (c *teamPermChecker) filter(t auth.Token) (*event.TargetFilter, error) {
	contexts := permission.ContextsForPermission(t, permission.PermTeamReadEvents)
	if len(contexts) == 0 {
		return nil, nil
	}
	allowed := event.TargetFilter{Type: event.TargetTypeTeam}
	for _, ctx := range contexts {
		if ctx.CtxType == permission.CtxGlobal {
			allowed.Values = nil
			break
		} else if ctx.CtxType == permission.CtxTeam {
			allowed.Values = append(allowed.Values, ctx.Value)
		}
	}
	return &allowed, nil
}

func (c *teamPermChecker) check(t auth.Token, r *http.Request, e *event.Event, kind checkKind) (bool, error) {
	tm, err := auth.GetTeam(e.Target.Value)
	if err != nil {
		return false, err
	}
	perms := map[checkKind]*permission.PermissionScheme{
		readCheckKind:   permission.PermTeamReadEvents,
		updateCheckKind: permission.PermTeamUpdateEvents,
	}
	hasPermission := permission.Check(
		t, perms[kind],
		permission.Context(permission.CtxTeam, tm.Name),
	)
	return hasPermission, nil
}

type servicePermChecker struct{}

func (c *servicePermChecker) filter(t auth.Token) (*event.TargetFilter, error) {
	contexts := permission.ContextsForPermission(t, permission.PermServiceReadEvents)
	if len(contexts) == 0 {
		return nil, nil
	}
	services, err := provisionReadableServices(t, contexts)
	if err != nil {
		return nil, err
	}
	if len(services) == 0 {
		return nil, nil
	}
	allowed := event.TargetFilter{Type: event.TargetTypeService}
	for _, s := range services {
		allowed.Values = append(allowed.Values, s.Name)
	}
	return &allowed, nil
}

func (c *servicePermChecker) check(t auth.Token, r *http.Request, e *event.Event, kind checkKind) (bool, error) {
	s, err := getService(e.Target.Value)
	if err != nil {
		return false, err
	}
	perms := map[checkKind]*permission.PermissionScheme{
		readCheckKind:   permission.PermServiceReadEvents,
		updateCheckKind: permission.PermServiceUpdateEvents,
	}
	hasPermission := permission.Check(t, perms[kind],
		append(permission.Contexts(permission.CtxTeam, s.OwnerTeams),
			permission.Context(permission.CtxService, s.Name),
		)...,
	)
	return hasPermission, nil
}

type serviceInstancePermChecker struct{}

func (c *serviceInstancePermChecker) filter(t auth.Token) (*event.TargetFilter, error) {
	contexts := permission.ContextsForPermission(t, permission.PermServiceInstanceReadEvents)
	if len(contexts) == 0 {
		return nil, nil
	}
	instances, err := readableInstances(t, contexts, "", "")
	if err != nil {
		return nil, err
	}
	if len(instances) == 0 {
		return nil, nil
	}
	allowed := event.TargetFilter{Type: event.TargetTypeServiceInstance}
	for _, s := range instances {
		allowed.Values = append(allowed.Values, serviceIntancePermName(s.ServiceName, s.Name))
	}
	return &allowed, nil
}

func (c *serviceInstancePermChecker) check(t auth.Token, r *http.Request, e *event.Event, kind checkKind) (bool, error) {
	var hasPermission bool
	if v := strings.SplitN(e.Target.Value, "/", 2); len(v) == 2 {
		si, err := getServiceInstanceOrError(v[0], v[1])
		if err != nil {
			return hasPermission, err
		}
		perms := map[checkKind]*permission.PermissionScheme{
			readCheckKind:   permission.PermServiceInstanceReadEvents,
			updateCheckKind: permission.PermServiceInstanceUpdateEvents,
		}
		hasPermission = permission.Check(t, perms[kind],
			append(permission.Contexts(permission.CtxTeam, si.Teams),
				permission.Context(permission.CtxServiceInstance, e.Target.Value),
			)...,
		)
	}
	return hasPermission, nil
}

type poolPermChecker struct{}

func (c *poolPermChecker) filter(t auth.Token) (*event.TargetFilter, error) {
	contexts := permission.ContextsForPermission(t, permission.PermPoolReadEvents)
	if len(contexts) == 0 {
		return nil, nil
	}
	allowed := event.TargetFilter{Type: event.TargetTypePool}
	for _, ctx := range contexts {
		if ctx.CtxType == permission.CtxGlobal {
			allowed.Values = nil
			break
		} else if ctx.CtxType == permission.CtxPool {
			allowed.Values = append(allowed.Values, ctx.Value)
		}
	}
	return &allowed, nil
}

func (c *poolPermChecker) check(t auth.Token, r *http.Request, e *event.Event, kind checkKind) (bool, error) {
	p, err := provision.GetPoolByName(e.Target.Value)
	if err != nil {
		return false, err
	}
	perms := map[checkKind]*permission.PermissionScheme{
		readCheckKind:   permission.PermPoolReadEvents,
		updateCheckKind: permission.PermPoolUpdateEvents,
	}
	hasPermission := permission.Check(
		t, perms[kind],
		permission.Context(permission.CtxPool, p.Name),
	)
	return hasPermission, nil
}

type userPermChecker struct{}

func (c *userPermChecker) filter(t auth.Token) (*event.TargetFilter, error) {
	allowed := event.TargetFilter{Type: event.TargetTypeUser, Values: []string{t.GetUserName()}}
	contexts := permission.ContextsForPermission(t, permission.PermUserReadEvents)
	if len(contexts) == 0 {
		return &allowed, nil
	}
	for _, ctx := range contexts {
		if ctx.CtxType == permission.CtxGlobal {
			allowed.Values = nil
			break
		}
	}
	return &allowed, nil
}

func (c *userPermChecker) check(t auth.Token, r *http.Request, e *event.Event, kind checkKind) (bool, error) {
	perms := map[checkKind]*permission.PermissionScheme{
		readCheckKind:   permission.PermUserReadEvents,
		updateCheckKind: permission.PermUserUpdateEvents,
	}
	return permission.Check(
		t, perms[kind],
		permission.Context(permission.CtxGlobal, ""),
	), nil
}

type iaasPermChecker struct{}

func (c *iaasPermChecker) filter(t auth.Token) (*event.TargetFilter, error) {
	contexts := permission.ContextsForPermission(t, permission.PermMachineReadEvents)
	if len(contexts) == 0 {
		return nil, nil
	}
	allowed := event.TargetFilter{Type: event.TargetTypeIaas}
	for _, ctx := range contexts {
		if ctx.CtxType == permission.CtxGlobal {
			allowed.Values = nil
			break
		} else if ctx.CtxType == permission.CtxIaaS {
			allowed.Values = append(allowed.Values, ctx.Value)
		}
	}
	return &allowed, nil
}

func (c *iaasPermChecker) check(t auth.Token, r *http.Request, e *event.Event, kind checkKind) (bool, error) {
	perms := map[checkKind]*permission.PermissionScheme{
		readCheckKind:   permission.PermMachineReadEvents,
		updateCheckKind: permission.PermMachineUpdateEvents,
	}
	return permission.Check(
		t, perms[kind],
		permission.Context(permission.CtxIaaS, e.Target.Value),
	), nil
}

type containerPermChecker struct{}

func (c *containerPermChecker) filter(t auth.Token) (*event.TargetFilter, error) {
	contexts := permission.ContextsForPermission(t, permission.PermAppReadEvents)
	if len(contexts) == 0 {
		return nil, nil
	}
	apps, err := app.List(appFilterByContext(contexts, nil))
	if err != nil {
		return nil, err
	}
	if len(apps) == 0 {
		return nil, nil
	}
	allowed := event.TargetFilter{Type: event.TargetTypeContainer, Values: []string{}}
	for _, a := range apps {
		units, err := a.Units()
		if err != nil {
			return nil, err
		}
		for _, u := range units {
			allowed.Values = append(allowed.Values, u.ID)
		}
	}
	return &allowed, nil
}

func (c *containerPermChecker) check(t auth.Token, r *http.Request, e *event.Event, kind checkKind) (bool, error) {
	a, err := app.Provisioner.GetAppFromUnitID(e.Target.Value)
	if err != nil {
		return false, err
	}
	perms := map[checkKind]*permission.PermissionScheme{
		readCheckKind:   permission.PermAppReadEvents,
		updateCheckKind: permission.PermAppUpdateEvents,
	}
	return permission.Check(t, perms[kind],
		append(permission.Contexts(permission.CtxTeam, a.GetTeamsName()),
			permission.Context(permission.CtxApp, a.GetName()),
			permission.Context(permission.CtxPool, a.GetPool()),
		)...,
	), nil
}

type nodePermChecker struct{}

func (c *nodePermChecker) filter(t auth.Token) (*event.TargetFilter, error) {
	contexts := permission.ContextsForPermission(t, permission.PermPoolReadEvents)
	if len(contexts) == 0 {
		return nil, nil
	}
	allowed := event.TargetFilter{Type: event.TargetTypeNode}
	var nodes []provision.Node
	var err error
	for _, ctx := range contexts {
		if ctx.CtxType == permission.CtxGlobal {
			allowed.Values = nil
			break
		} else if ctx.CtxType == permission.CtxPool {
			if nodes == nil {
				if nodeProvisioner, ok := app.Provisioner.(provision.NodeProvisioner); ok {
					nodes, err = nodeProvisioner.ListNodes(nil)
					if err != nil {
						return nil, err
					}
				}
			}
			for _, n := range nodes {
				if n.Pool() == ctx.Value {
					allowed.Values = append(allowed.Values, n.Address())
				}
			}
		}
	}
	return &allowed, nil
}

func (c *nodePermChecker) check(t auth.Token, r *http.Request, e *event.Event, kind checkKind) (bool, error) {
	var hasPermission bool
	if nodeProvisioner, ok := app.Provisioner.(provision.NodeProvisioner); ok {
		var ctx []permission.PermissionContext
		nodes, err := nodeProvisioner.ListNodes([]string{e.Target.Value})
		if err != nil {
			return false, err
		}
		if len(nodes) > 0 {
			ctx = append(ctx, permission.Context(permission.CtxPool, nodes[0].Pool()))
		}
		perms := map[checkKind]*permission.PermissionScheme{
			readCheckKind:   permission.PermPoolReadEvents,
			updateCheckKind: permission.PermPoolUpdateEvents,
		}
		hasPermission = permission.Check(
			t, perms[kind],
			ctx...,
		)
	}
	return hasPermission, nil
}

type rolePermChecker struct{}

func (c *rolePermChecker) filter(t auth.Token) (*event.TargetFilter, error) {
	contexts := permission.ContextsForPermission(t, permission.PermRoleReadEvents)
	if len(contexts) == 0 {
		return nil, nil
	}
	allowed := event.TargetFilter{Type: event.TargetTypeRole}
	for _, ctx := range contexts {
		if ctx.CtxType == permission.CtxGlobal {
			allowed.Values = nil
			break
		}
	}
	return &allowed, nil
}

func (c *rolePermChecker) check(t auth.Token, r *http.Request, e *event.Event, kind checkKind) (bool, error) {
	perms := map[checkKind]*permission.PermissionScheme{
		readCheckKind:   permission.PermRoleReadEvents,
		updateCheckKind: permission.PermRoleUpdateEvents,
	}
	return permission.Check(
		t, perms[kind],
		permission.Context(permission.CtxGlobal, ""),
	), nil
}

func filterForPerms(t auth.Token, filter *event.Filter) (*event.Filter, error) {
	if filter == nil {
		filter = &event.Filter{}
	}
	filter.AllowedTargets = []event.TargetFilter{}
	for _, checker := range evtPermMap {
		allowed, err := checker.filter(t)
		if err != nil {
			return nil, err
		}
		if allowed != nil {
			filter.AllowedTargets = append(filter.AllowedTargets, *allowed)
		}
	}
	return filter, nil
}

// title: event list
// path: /events
// method: GET
// produce: application/json
// responses:
//   200: OK
//   204: No content
func eventList(w http.ResponseWriter, r *http.Request, t auth.Token) error {
	r.ParseForm()
	filter := &event.Filter{}
	dec := form.NewDecoder(nil)
	dec.IgnoreUnknownKeys(true)
	dec.IgnoreCase(true)
	err := dec.DecodeValues(&filter, r.Form)
	if err != nil {
		return &errors.HTTP{Code: http.StatusBadRequest, Message: fmt.Sprintf("unable to parse event filters: %s", err)}
	}
	filter.PruneUserValues()
	filter, err = filterForPerms(t, filter)
	if err != nil {
		return err
	}
	events, err := event.List(filter)
	if err != nil {
		return err
	}
	if len(events) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	w.Header().Add("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(events)
}

// title: kind list
// path: /events/kinds
// method: GET
// produce: application/json
// responses:
//   200: OK
//   204: No content
func kindList(w http.ResponseWriter, r *http.Request, t auth.Token) error {
	kinds, err := event.GetKinds()
	if err != nil {
		return err
	}
	if len(kinds) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	w.Header().Add("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(kinds)
}

// title: event info
// path: /events/{uuid}
// method: GET
// produce: application/json
// responses:
//   200: OK
//   400: Invalid uuid
//   401: Unauthorized
//   404: Not found
func eventInfo(w http.ResponseWriter, r *http.Request, t auth.Token) error {
	uuid := r.URL.Query().Get(":uuid")
	if !bson.IsObjectIdHex(uuid) {
		msg := fmt.Sprintf("uuid parameter is not ObjectId: %s", uuid)
		return &errors.HTTP{Code: http.StatusBadRequest, Message: msg}
	}
	objID := bson.ObjectIdHex(uuid)
	e, err := event.GetByID(objID)
	if err != nil {
		return &errors.HTTP{Code: http.StatusNotFound, Message: err.Error()}
	}
	checker := evtPermMap[e.Target.Type]
	if checker == nil {
		return permission.ErrUnauthorized
	}
	hasPermission, err := checker.check(t, r, e, readCheckKind)
	if err != nil {
		return err
	}
	if !hasPermission {
		return permission.ErrUnauthorized
	}
	w.Header().Add("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(e)
}

// title: event cancel
// path: /events/{uuid}/cancel
// method: POST
// produce: application/json
// responses:
//   200: OK
//   400: Invalid uuid or empty reason
//   404: Not found
func eventCancel(w http.ResponseWriter, r *http.Request, t auth.Token) error {
	uuid := r.URL.Query().Get(":uuid")
	if !bson.IsObjectIdHex(uuid) {
		msg := fmt.Sprintf("uuid parameter is not ObjectId: %s", uuid)
		return &errors.HTTP{Code: http.StatusBadRequest, Message: msg}
	}
	objID := bson.ObjectIdHex(uuid)
	e, err := event.GetByID(objID)
	if err != nil {
		return &errors.HTTP{Code: http.StatusNotFound, Message: err.Error()}
	}
	reason := r.FormValue("reason")
	if reason == "" {
		return &errors.HTTP{Code: http.StatusBadRequest, Message: "reason is mandatory"}
	}
	checker := evtPermMap[e.Target.Type]
	if checker == nil {
		return permission.ErrUnauthorized
	}
	hasPermission, err := checker.check(t, r, e, updateCheckKind)
	if err != nil {
		return err
	}
	if !hasPermission {
		return permission.ErrUnauthorized
	}
	err = e.TryCancel(reason, t.GetUserName())
	if err != nil {
		if err == event.ErrNotCancelable {
			return &errors.HTTP{Code: http.StatusBadRequest, Message: err.Error()}
		}
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
