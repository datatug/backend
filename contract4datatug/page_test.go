package contract4datatug_test

// This file checks this repository's own page against its own fixtures. It is
// not copied into a repository that uses the fixtures.

import (
	"os"
	"strings"
	"testing"
)

const pagePath = "../spec/features/plans-and-ai-metering/README.md"

// Every fixture is described on the contract page, so a fixture cannot be
// added, or renamed, without the page saying what it is.
func TestPageNamesEveryFixture(t *testing.T) {
	names, _, err := readChecksums(fixturesDir)
	if err != nil {
		t.Fatal(err)
	}
	page, err := os.ReadFile(pagePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if !strings.Contains(string(page), "`"+name+"`") {
			t.Errorf("the contract page does not name the fixture `%s`", name)
		}
	}
}
