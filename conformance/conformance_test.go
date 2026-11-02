package conformance

import (
	"flag"
	"testing"
)

// The fixtures live in spec/conformance in the repository root; the
// package directory is one level below it.
var fixtureDir = flag.String("fixture-dir", "../spec/conformance", "the conformance fixture directory")

// TestConformanceFixtures is the reference runner: it walks the index,
// builds each fixture CLI from its declaration, and asserts every case.
// A failure names the case and what disagreed, so a port author can read
// the delta without reading the runner.
func TestConformanceFixtures(t *testing.T) {
	fixtures, err := LoadIndex(*fixtureDir)
	if err != nil {
		t.Fatalf("load index: %v", err)
	}
	for _, fixture := range fixtures {
		fixture := fixture
		t.Run(fixture.ID, func(t *testing.T) {
			failures := CheckFixture(&fixture)
			for _, f := range failures {
				t.Errorf("%s: %s", f.Case, f.Details)
			}
		})
	}
}
