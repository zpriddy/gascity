// MySQL backend doctor check — detects two kinds of drift in mysql-backed
// cities and offers --fix paths:
//
//   1. Connection drift: backend=mysql in metadata.json but mysqld at the
//      configured host:port is unreachable.
//      Fix: nothing automatic — operator should start mysqld (or rely on
//      the supervisor's autostart hook). The check still surfaces the
//      problem clearly.
//
//   2. Backend drift: metadata.json says backend=dolt but config.yaml has
//      mysql.* keys *and* dolt is unreachable. This is the "mid-migration
//      bricked" state we discovered on scs-core-services. Fix flips the
//      metadata to mysql, scrubs dolt fields, and re-canonicalises both
//      files using the mysql.* values from config.yaml.
//
// Both branches are skipped for postgres-backend or empty-backend scopes.
package main

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"time"

	"github.com/gastownhall/gascity/internal/beads/contract"
	"github.com/gastownhall/gascity/internal/doctor"
	"github.com/gastownhall/gascity/internal/fsys"
)

// mysqlBackendCheck implements the doctor.Check interface for mysql-backed
// scopes (city + each rig that points at the city's database).
type mysqlBackendCheck struct {
	cityPath string

	// Computed during Run; consumed by Fix.
	driftKind mysqlDriftKind
	target    string // host:port of the backend in question (for connection drift)
	cfgState  contract.ConfigState
}

type mysqlDriftKind int

const (
	mysqlDriftNone       mysqlDriftKind = iota
	mysqlDriftConnection                // backend=mysql but server unreachable
	mysqlDriftBackend                   // backend=dolt in metadata, mysql.* in config
)

func newMysqlBackendCheck(cityPath string) *mysqlBackendCheck {
	return &mysqlBackendCheck{cityPath: cityPath}
}

// Name implements doctor.Check.
func (c *mysqlBackendCheck) Name() string { return "mysql-backend" }

// WarmupEligible implements doctor.Check. The check is cheap (one TCP probe
// + one file read) and worth running on `gc start`.
func (c *mysqlBackendCheck) WarmupEligible() bool { return true }

// Run implements doctor.Check.
func (c *mysqlBackendCheck) Run(_ *doctor.CheckContext) *doctor.CheckResult {
	fs := fsys.OSFS{}
	beadsDir := filepath.Join(c.cityPath, ".beads")
	metaPath := filepath.Join(beadsDir, "metadata.json")
	cfgPath := filepath.Join(beadsDir, "config.yaml")

	meta, metaOK, _ := contract.LoadMetadataState(fs, metaPath)
	cfg, cfgOK, _ := contract.ReadConfigState(fs, cfgPath)

	if !metaOK && !cfgOK {
		// Not a bd-style scope; let other checks handle it.
		return &doctor.CheckResult{Name: c.Name(), Status: doctor.StatusOK, Message: "no .beads/ scope to check"}
	}

	// Backend-drift detection: metadata says dolt-or-empty but config has
	// mysql.* keys. Trust the config in that case (operator wrote config but
	// metadata.json wasn't flipped — exactly the scs-core-services bug).
	if cfgOK && cfg.Backend == "mysql" && metaOK && meta.Backend != "mysql" {
		c.driftKind = mysqlDriftBackend
		c.cfgState = cfg
		return &doctor.CheckResult{
			Name:    c.Name(),
			Status:  doctor.StatusError,
			Message: fmt.Sprintf("metadata.json says backend=%q but config.yaml has mysql.* keys (mid-migration drift)", meta.Backend),
			FixHint: "run with --fix to flip metadata.json to backend=mysql and scrub dolt fields",
		}
	}

	// Connection-drift detection: backend=mysql but mysqld not reachable.
	if metaOK && meta.Backend == "mysql" {
		host := meta.MysqlHost
		port := meta.MysqlPort
		if host == "" && cfgOK {
			host = cfg.MysqlHost
		}
		if port == "" && cfgOK {
			port = cfg.MysqlPort
		}
		c.target = net.JoinHostPort(host, port)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if mysqlAutostartProbe(ctx, host, port) {
			return &doctor.CheckResult{
				Name:    c.Name(),
				Status:  doctor.StatusOK,
				Message: fmt.Sprintf("mysql backend reachable at %s", c.target),
			}
		}
		c.driftKind = mysqlDriftConnection
		return &doctor.CheckResult{
			Name:    c.Name(),
			Status:  doctor.StatusError,
			Message: fmt.Sprintf("mysql backend at %s is not reachable", c.target),
			FixHint: "start mysqld (the supervisor's autostart hook will try this on `gc supervisor start`); --fix is not automatic for connection drift",
		}
	}

	return &doctor.CheckResult{
		Name:    c.Name(),
		Status:  doctor.StatusOK,
		Message: "scope is not mysql-backed; nothing to check",
	}
}

// CanFix implements doctor.Check. Only backend-drift is auto-fixable;
// connection-drift requires the operator to start mysqld.
func (c *mysqlBackendCheck) CanFix() bool {
	return c.driftKind == mysqlDriftBackend
}

// Fix implements doctor.Check. Reads the mysql.* keys from config.yaml,
// rewrites metadata.json with backend=mysql, scrubs dolt fields, and
// re-canonicalises the config (which is largely a no-op since config is
// already mysql-shaped — but the writer also strips deprecated keys).
func (c *mysqlBackendCheck) Fix(_ *doctor.CheckContext) error {
	if c.driftKind != mysqlDriftBackend {
		return fmt.Errorf("mysql-backend check has nothing to fix")
	}
	fs := fsys.OSFS{}
	beadsDir := filepath.Join(c.cityPath, ".beads")
	metaPath := filepath.Join(beadsDir, "metadata.json")
	cfgPath := filepath.Join(beadsDir, "config.yaml")

	cfg, ok, err := contract.ReadConfigState(fs, cfgPath)
	if err != nil || !ok {
		return fmt.Errorf("read config.yaml: %v", err)
	}
	if cfg.MysqlDatabase == "" {
		return fmt.Errorf("config.yaml is missing mysql.database; cannot determine target")
	}

	target := contract.MetadataState{
		Database:      cfg.MysqlDatabase,
		Backend:       "mysql",
		MysqlHost:     cfg.MysqlHost,
		MysqlPort:     cfg.MysqlPort,
		MysqlUser:     cfg.MysqlUser,
		MysqlDatabase: cfg.MysqlDatabase,
	}
	if _, err := contract.EnsureCanonicalMetadata(fs, metaPath, target); err != nil {
		return fmt.Errorf("rewrite metadata.json: %v", err)
	}
	// Re-canonicalise config too — picks up any drift in deprecated keys.
	cfgState := cfg
	cfgState.Backend = "mysql"
	if _, err := contract.EnsureCanonicalConfig(fs, cfgPath, cfgState); err != nil {
		return fmt.Errorf("rewrite config.yaml: %v", err)
	}
	return nil
}
