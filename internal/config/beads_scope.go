package config

import (
	"path/filepath"
	"strings"
)

const (
	RigBeadsScopeLegacy = "legacy"
	RigBeadsScopeCity   = "city"
)

func EffectiveRigBeadsScope(rig Rig) string {
	if rig.SharedAcrossCities {
		return RigBeadsScopeCity
	}
	scope := strings.TrimSpace(rig.BeadsScope)
	if scope == "" {
		return RigBeadsScopeLegacy
	}
	return scope
}

func CityBeadsScopeRoot(cityPath string) string {
	return filepath.Join(cityPath, ".beads")
}

func RigBeadsScopeRoot(cityName string, rig Rig) string {
	switch EffectiveRigBeadsScope(rig) {
	case RigBeadsScopeCity:
		return filepath.Join(rig.Path, ".beads", "cities", cityName)
	default:
		return filepath.Join(rig.Path, ".beads")
	}
}

func BeadsMetadataPathForRoot(scopeRoot string) string {
	return filepath.Join(scopeRoot, "metadata.json")
}

func BeadsConfigPathForRoot(scopeRoot string) string {
	return filepath.Join(scopeRoot, "config.yaml")
}

func BeadsHooksPathForRoot(scopeRoot string) string {
	return filepath.Join(scopeRoot, "hooks")
}

func BeadsPortFilePathForRoot(scopeRoot string) string {
	return filepath.Join(scopeRoot, "dolt-server.port")
}
