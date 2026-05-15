// session_resolve.go provides CLI-level session resolution.
// The core resolution logic lives in internal/session.ResolveSessionID.
package main

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/session"
)

// resolveSessionID delegates to session.ResolveSessionID.
func resolveSessionID(store beads.Store, identifier string) (string, error) {
	return session.ResolveSessionID(store, identifier)
}

func resolveSessionIDAllowClosed(store beads.Store, identifier string) (string, error) {
	return session.ResolveSessionIDAllowClosed(store, identifier)
}

type namedSessionResolveOptions struct {
	allowClosed         bool
	materialize         bool
	materializeMetadata map[string]string
}

const templateTargetPrefix = "template:"

type templateTarget struct {
	template   string
	forceFresh bool
}

var errNamedSessionConflict = errors.New("configured named session conflict")

func resolveConfiguredNamedSessionID(
	cityPath string,
	cfg *config.City,
	store beads.Store,
	identifier string,
	opts namedSessionResolveOptions,
) (string, bool, error) {
	if cfg == nil || store == nil {
		return "", false, fmt.Errorf("%w: %q", session.ErrSessionNotFound, identifier)
	}
	cityName := config.EffectiveCityName(cfg, filepath.Base(cityPath))
	spec, ok, err := findNamedSessionSpecForTarget(cfg, cityName, identifier)
	if err != nil {
		return "", false, err
	}
	if !ok {
		return "", false, fmt.Errorf("%w: %q", session.ErrSessionNotFound, identifier)
	}
	lookup, err := session.LookupConfiguredNamedSession(store, spec)
	if err != nil {
		return "", true, fmt.Errorf("looking up configured named session: %w", err)
	}
	if lookup.HasCanonical {
		return lookup.Canonical.ID, true, nil
	}
	// When materializing, check for a closed bead with this identity and
	// reopen it (preserves bead ID for reference continuity).
	if opts.materialize {
		if bead, ok := reopenClosedConfiguredNamedSessionBead(
			cityPath, store, cfg, cityName, spec.Identity, spec.SessionName, "stopped", time.Now().UTC(), opts.materializeMetadata, io.Discard,
		); ok {
			return bead.ID, true, nil
		}
	}
	if lookup.HasConflict {
		return "", true, fmt.Errorf("%w: %q conflicts with configured named session %q via live bead %s", errNamedSessionConflict, identifier, spec.Identity, lookup.Conflict.ID)
	}
	if !opts.materialize {
		return "", false, fmt.Errorf("%w: %q", session.ErrSessionNotFound, identifier)
	}
	id, err := ensureSessionIDForTemplateWithOptions(cityPath, cfg, store, spec.Identity, io.Discard, ensureSessionForTemplateOptions{
		materializeMetadata: opts.materializeMetadata,
	})
	return id, true, err
}

func resolveSessionIDWithConfig(cityPath string, cfg *config.City, store beads.Store, identifier string) (string, error) {
	return resolveSessionIDWithOptions(cityPath, cfg, store, identifier, namedSessionResolveOptions{})
}

func resolveSessionIDAllowClosedWithConfig(cityPath string, cfg *config.City, store beads.Store, identifier string) (string, error) {
	return resolveSessionIDWithOptions(cityPath, cfg, store, identifier, namedSessionResolveOptions{allowClosed: true})
}

func resolveSessionIDMaterializingNamed(cityPath string, cfg *config.City, store beads.Store, identifier string) (string, error) {
	return resolveSessionIDWithOptions(cityPath, cfg, store, identifier, namedSessionResolveOptions{materialize: true})
}

func resolveSessionIDMaterializingNamedWithMetadata(cityPath string, cfg *config.City, store beads.Store, identifier string, metadata map[string]string) (string, error) {
	return resolveSessionIDWithOptions(cityPath, cfg, store, identifier, namedSessionResolveOptions{
		materialize:         true,
		materializeMetadata: metadata,
	})
}

func parseTemplateTarget(identifier string) (templateTarget, bool) {
	identifier = strings.TrimSpace(identifier)
	if !strings.HasPrefix(identifier, templateTargetPrefix) {
		return templateTarget{}, false
	}
	name := normalizeNamedSessionTarget(strings.TrimSpace(strings.TrimPrefix(identifier, templateTargetPrefix)))
	if name == "" {
		return templateTarget{}, false
	}
	return templateTarget{
		template:   name,
		forceFresh: true,
	}, true
}

func resolveSessionIDWithOptions(
	cityPath string,
	cfg *config.City,
	store beads.Store,
	identifier string,
	opts namedSessionResolveOptions,
) (string, error) {
	if store == nil {
		return "", fmt.Errorf("session store unavailable")
	}
	if _, ok := parseTemplateTarget(identifier); ok {
		return "", fmt.Errorf("%w: %q", session.ErrSessionNotFound, identifier)
	}
	if id, err := session.ResolveSessionIDByExactID(store, identifier); err == nil {
		return id, nil
	} else if !errors.Is(err, session.ErrSessionNotFound) {
		return "", err
	}
	if id, matched, err := resolveConfiguredNamedSessionID(cityPath, cfg, store, identifier, opts); err == nil {
		return id, nil
	} else if matched || !errors.Is(err, session.ErrSessionNotFound) {
		return "", fmt.Errorf("resolving configured named session %q: %w", identifier, err)
	}
	if id, err := session.ResolveSessionID(store, identifier); err == nil {
		if cfg != nil {
			if bead, getErr := store.Get(id); getErr == nil && isNamedSessionBead(bead) {
				identity := namedSessionIdentity(bead)
				if identity != "" && config.FindNamedSession(cfg, identity) == nil {
					return "", fmt.Errorf("%w: %q", session.ErrSessionNotFound, identifier)
				}
			}
		}
		return id, nil
	} else if !errors.Is(err, session.ErrSessionNotFound) {
		return "", err
	}
	if id, err := resolveOpenQualifiedAliasBasename(store, identifier); err == nil {
		return id, nil
	} else if !errors.Is(err, session.ErrSessionNotFound) {
		return "", err
	}
	// Fall back to title-based matching (path-alias): pool sessions surface
	// their qualified agent name as the bead Title, which is the stable
	// identifier operators use (e.g. "mayor", "my-rig/claude"). The API layer
	// resolves these via resolveLiveSessionByPathAlias; wire the same logic
	// for CLI commands like `gc mail send` and `gc session nudge`.
	if id, ok, err := resolveLiveSessionByTitle(store, identifier); err != nil {
		return "", err
	} else if ok {
		return id, nil
	}
	if opts.allowClosed {
		if cfg != nil {
			cityName := config.EffectiveCityName(cfg, filepath.Base(cityPath))
			if _, ok, err := findNamedSessionSpecForTarget(cfg, cityName, identifier); err != nil {
				return "", err
			} else if ok {
				return "", fmt.Errorf("%w: %q", session.ErrSessionNotFound, identifier)
			}
		}
		if id, err := session.ResolveSessionIDAllowClosed(store, identifier); err == nil {
			return id, nil
		} else if !errors.Is(err, session.ErrSessionNotFound) {
			return "", err
		}
	}
	return "", fmt.Errorf("%w: %q", session.ErrSessionNotFound, identifier)
}

func resolveOpenQualifiedAliasBasename(store beads.Store, identifier string) (string, error) {
	identifier = strings.TrimSpace(identifier)
	if store == nil || identifier == "" || strings.Contains(identifier, "/") {
		return "", fmt.Errorf("%w: %q", session.ErrSessionNotFound, identifier)
	}
	all, err := session.ListAllSessionBeads(store, beads.ListQuery{})
	if err != nil {
		return "", fmt.Errorf("listing sessions: %w", err)
	}
	matches := make([]beads.Bead, 0, 1)
	for _, b := range all {
		// ListAllSessionBeads already filters via IsSessionBeadOrRepairable.
		if b.Status == "closed" {
			continue
		}
		session.RepairEmptyType(store, &b)
		alias := strings.TrimSpace(b.Metadata["alias"])
		if alias == "" || !strings.Contains(alias, "/") || session.TargetBasename(alias) != identifier {
			continue
		}
		matches = append(matches, b)
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("%w: %q", session.ErrSessionNotFound, identifier)
	case 1:
		return matches[0].ID, nil
	default:
		labels := make([]string, 0, len(matches))
		for _, match := range matches {
			labels = append(labels, fmt.Sprintf("%s (%s)", match.ID, strings.TrimSpace(match.Metadata["alias"])))
		}
		return "", fmt.Errorf("%w: %q matches %d sessions: %s", session.ErrAmbiguous, identifier, len(matches), strings.Join(labels, ", "))
	}
}

// resolveLiveSessionByTitle matches identifier against the Title of an active
// pool-session bead. Pool sessions surface their qualified agent name as the
// bead Title (the same string `gc session list` shows under TARGET/TITLE).
// Their session_name is a synthetic internal ID (s-gc-NNN) invisible to
// session.ResolveSessionID's session_name/alias indexes.
//
// State filter: active, awake, and empty (legacy none) states match.
// Configured named-session beads are skipped so the named-session resolver
// chain still owns those identifiers.
//
// On duplicate matching Titles (rare misconfiguration), the most recently
// created bead wins.
func resolveLiveSessionByTitle(store beads.Store, identifier string) (string, bool, error) {
	if store == nil {
		return "", false, nil
	}
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return "", false, nil
	}
	all, err := store.List(beads.ListQuery{Label: session.LabelSession})
	if err != nil {
		return "", false, fmt.Errorf("resolveLiveSessionByTitle: listing sessions: %w", err)
	}
	var best beads.Bead
	found := false
	for _, b := range all {
		if !session.IsSessionBeadOrRepairable(b) {
			continue
		}
		if session.IsNamedSessionBead(b) {
			continue
		}
		if strings.TrimSpace(b.Title) != identifier {
			continue
		}
		state := session.State(b.Metadata["state"])
		if state != session.StateActive && state != session.StateAsleep && state != session.StateNone {
			continue
		}
		if !found || b.CreatedAt.After(best.CreatedAt) {
			best = b
			found = true
		}
	}
	if !found {
		return "", false, nil
	}
	return best.ID, true, nil
}
