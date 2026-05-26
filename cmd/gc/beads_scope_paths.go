package main

import "github.com/gastownhall/gascity/internal/config"

func effectiveRigBeadsScope(rig config.Rig) string { return config.EffectiveRigBeadsScope(rig) }
func cityBeadsScopeRoot(cityPath string) string    { return config.CityBeadsScopeRoot(cityPath) }
func rigBeadsScopeRoot(cityName string, rig config.Rig) string {
	return config.RigBeadsScopeRoot(cityName, rig)
}
func beadsMetadataPathForRoot(scopeRoot string) string {
	return config.BeadsMetadataPathForRoot(scopeRoot)
}
func beadsConfigPathForRoot(scopeRoot string) string { return config.BeadsConfigPathForRoot(scopeRoot) }
func beadsHooksPathForRoot(scopeRoot string) string  { return config.BeadsHooksPathForRoot(scopeRoot) }
func beadsPortFilePathForRoot(scopeRoot string) string {
	return config.BeadsPortFilePathForRoot(scopeRoot)
}
