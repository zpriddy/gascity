package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
)

// mysqlOrphanedRigStore is a placeholder Store for rigs that declared
// `endpoint_origin: inherited_city` (per-rig .beads/config.yaml) and
// `backend: dolt` (per-rig .beads/metadata.json) — but whose enclosing
// city has migrated to `backend: mysql`. The rig's data lives in a Dolt
// database that is no longer started by gc, and bd cannot reach it. The
// rig's metadata is the operator's responsibility to update; until they
// do, gc should NOT spam supervisor logs with `bd ready: exit status 1:
// Dolt server unreachable at 127.0.0.1:0` on every tick.
//
// Read calls return empty (no error) so scaleCheck / collectAssignedWork
// proceed without PARTIAL escalations. Write calls return a clear error
// so any attempt to mutate this rig surfaces an actionable message
// instead of corrupting state silently.
type mysqlOrphanedRigStore struct {
	rigName string
	rigPath string
}

func newMysqlOrphanedRigStore(rigName, rigPath string) mysqlOrphanedRigStore {
	return mysqlOrphanedRigStore{rigName: rigName, rigPath: rigPath}
}

func (s mysqlOrphanedRigStore) writeErr() error {
	return fmt.Errorf("rig %q at %s: city migrated to mysql but rig metadata still declares backend=dolt — update %s/.beads/metadata.json to backend=mysql, OR remove the rig from city.toml",
		s.rigName, s.rigPath, s.rigPath)
}

func (s mysqlOrphanedRigStore) Create(beads.Bead) (beads.Bead, error) {
	return beads.Bead{}, s.writeErr()
}
func (s mysqlOrphanedRigStore) Get(string) (beads.Bead, error)  { return beads.Bead{}, nil }
func (s mysqlOrphanedRigStore) Update(string, beads.UpdateOpts) error { return s.writeErr() }
func (s mysqlOrphanedRigStore) Close(string) error                    { return s.writeErr() }
func (s mysqlOrphanedRigStore) Reopen(string) error                   { return s.writeErr() }
func (s mysqlOrphanedRigStore) CloseAll([]string, map[string]string) (int, error) {
	return 0, s.writeErr()
}
func (s mysqlOrphanedRigStore) List(beads.ListQuery) ([]beads.Bead, error)      { return nil, nil }
func (s mysqlOrphanedRigStore) ListOpen(...string) ([]beads.Bead, error)        { return nil, nil }
func (s mysqlOrphanedRigStore) Ready(...beads.ReadyQuery) ([]beads.Bead, error) { return nil, nil }
func (s mysqlOrphanedRigStore) Children(string, ...beads.QueryOpt) ([]beads.Bead, error) {
	return nil, nil
}

func (s mysqlOrphanedRigStore) ListByLabel(string, int, ...beads.QueryOpt) ([]beads.Bead, error) {
	return nil, nil
}

func (s mysqlOrphanedRigStore) ListByAssignee(string, string, int) ([]beads.Bead, error) {
	return nil, nil
}

func (s mysqlOrphanedRigStore) ListByMetadata(map[string]string, int, ...beads.QueryOpt) ([]beads.Bead, error) {
	return nil, nil
}
func (s mysqlOrphanedRigStore) SetMetadata(string, string, string) error         { return s.writeErr() }
func (s mysqlOrphanedRigStore) SetMetadataBatch(string, map[string]string) error { return s.writeErr() }
func (s mysqlOrphanedRigStore) Tx(string, func(beads.Tx) error) error            { return s.writeErr() }
func (s mysqlOrphanedRigStore) Delete(string) error                              { return s.writeErr() }
func (s mysqlOrphanedRigStore) Ping() error                                      { return nil }
func (s mysqlOrphanedRigStore) DepAdd(string, string, string) error              { return s.writeErr() }
func (s mysqlOrphanedRigStore) DepRemove(string, string) error                   { return s.writeErr() }
func (s mysqlOrphanedRigStore) DepList(string, string) ([]beads.Dep, error)      { return nil, nil }

var _ beads.Store = mysqlOrphanedRigStore{}

// rigOrphanedFromMySQLCity reports whether a rig is in a state where its
// own metadata.json declares backend=dolt, its config.yaml has
// endpoint_origin=inherited_city, but the parent city has migrated to
// backend=mysql. In this state bd subprocess calls at the rig dir fail
// loudly because the inherited city Dolt server is no longer running.
func rigOrphanedFromMySQLCity(cityPath, rigPath string) bool {
	if !cityUsesMySQLBackend(cityPath) {
		return false
	}
	if cityUsesMySQLBackend(rigPath) {
		// Rig's own metadata is mysql — properly migrated.
		return false
	}
	// Rig's metadata.json declares some non-mysql backend (dolt, postgres,
	// or absent). For dolt-declared rigs with endpoint_origin=inherited_city,
	// gc has nothing to inherit (no managed Dolt). Only treat as orphaned
	// when both conditions hold:
	//   1. metadata.json says backend=dolt (or empty, which defaults to dolt)
	//   2. config.yaml says endpoint_origin=inherited_city
	if !rigDeclaresDoltBackend(rigPath) {
		return false
	}
	if !rigEndpointInheritedFromCity(rigPath) {
		return false
	}
	return true
}

// rigDeclaresDoltBackend reports whether the rig's .beads/metadata.json
// declares backend=dolt (explicitly or by default — empty backend defaults
// to dolt in the current bd contract).
func rigDeclaresDoltBackend(rigPath string) bool {
	metaPath := filepath.Join(rigPath, ".beads", "metadata.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return false
	}
	var meta struct {
		Backend string `json:"backend"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return false
	}
	backend := strings.ToLower(strings.TrimSpace(meta.Backend))
	return backend == "" || backend == "dolt"
}

// rigEndpointInheritedFromCity reports whether the rig's
// .beads/config.yaml has gc.endpoint_origin: inherited_city. Uses a
// minimal direct line scan to avoid pulling the full contract reader
// into the dolt-only short-circuit path.
func rigEndpointInheritedFromCity(rigPath string) bool {
	cfgPath := filepath.Join(rigPath, ".beads", "config.yaml")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Accept both "gc.endpoint_origin: inherited_city" (flat) and
		// "endpoint_origin: inherited_city" under a "gc:" parent. The
		// flat form is what gc writes today.
		if strings.HasPrefix(line, "gc.endpoint_origin:") || strings.HasPrefix(line, "endpoint_origin:") {
			val := strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
			val = strings.Trim(val, `"' `)
			if strings.EqualFold(val, "inherited_city") {
				return true
			}
		}
	}
	return false
}
