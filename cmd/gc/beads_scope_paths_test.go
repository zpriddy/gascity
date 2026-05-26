package main

import (
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

func TestRigBeadsScopeRootLegacy(t *testing.T) {
	rig := config.Rig{Name: "app", Path: "/rigs/app"}
	got := rigBeadsScopeRoot("alpha-city", rig)
	want := "/rigs/app/.beads"
	if got != want {
		t.Fatalf("rigBeadsScopeRoot() = %q, want %q", got, want)
	}
}

func TestRigBeadsScopeRootCityScoped(t *testing.T) {
	rig := config.Rig{Name: "app", Path: "/rigs/app", BeadsScope: "city"}
	got := rigBeadsScopeRoot("alpha-city", rig)
	want := "/rigs/app/.beads/cities/alpha-city"
	if got != want {
		t.Fatalf("rigBeadsScopeRoot() = %q, want %q", got, want)
	}
}

func TestRigBeadsScopeRootSharedAcrossCitiesImpliesCityScope(t *testing.T) {
	rig := config.Rig{Name: "app", Path: "/rigs/app", SharedAcrossCities: true}
	got := rigBeadsScopeRoot("alpha-city", rig)
	want := "/rigs/app/.beads/cities/alpha-city"
	if got != want {
		t.Fatalf("rigBeadsScopeRoot() = %q, want %q", got, want)
	}
}
