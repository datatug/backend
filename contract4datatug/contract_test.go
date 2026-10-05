package contract4datatug

// This file is self-contained (standard library only) so that a repository
// that copies the fixtures of testdata/contract copies this file with them and
// checks its copies against the same CHECKSUMS file.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const (
	fixturesDir   = "../testdata/contract"
	checksumsName = "CHECKSUMS"
	pagePath      = "../spec/features/plans-and-ai-metering/README.md"
)

// readChecksums parses a CHECKSUMS file: one line per fixture, the SHA-256 of
// the file as 64 lower-case hex digits, two spaces, the file name; sorted by
// name; no blank line; ends with a line feed. `shasum -a 256 -c CHECKSUMS`
// reads the same file.
func readChecksums(dir string) (names []string, digests map[string]string, err error) {
	raw, err := os.ReadFile(filepath.Join(dir, checksumsName))
	if err != nil {
		return nil, nil, err
	}
	text := string(raw)
	if text == "" || !strings.HasSuffix(text, "\n") {
		return nil, nil, fmt.Errorf("%s must be non-empty and end with a line feed", checksumsName)
	}
	digests = map[string]string{}
	for i, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		digest, name, ok := strings.Cut(line, "  ")
		if !ok || len(digest) != 64 || strings.ToLower(digest) != digest || strings.Trim(digest, "0123456789abcdef") != "" {
			return nil, nil, fmt.Errorf("%s line %d is not '<sha256 hex>  <name>'", checksumsName, i+1)
		}
		if name == "" || strings.ContainsAny(name, `/\`) || !strings.HasSuffix(name, ".json") {
			return nil, nil, fmt.Errorf("%s line %d: %q is not a .json file name", checksumsName, i+1, name)
		}
		if _, dup := digests[name]; dup {
			return nil, nil, fmt.Errorf("%s names %s twice", checksumsName, name)
		}
		digests[name] = digest
		names = append(names, name)
	}
	if !sort.StringsAreSorted(names) {
		return nil, nil, fmt.Errorf("%s is not sorted by file name", checksumsName)
	}
	return names, digests, nil
}

// verifyFixtures checks that the .json files of dir are exactly the files
// CHECKSUMS names, that each is valid JSON, and that each matches its digest.
func verifyFixtures(dir string) error {
	names, digests, err := readChecksums(dir)
	if err != nil {
		return err
	}
	onDisk, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return err
	}
	for _, p := range onDisk {
		if _, ok := digests[filepath.Base(p)]; !ok {
			return fmt.Errorf("%s is not listed in %s", filepath.Base(p), checksumsName)
		}
	}
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("%s is listed in %s but cannot be read: %w", name, checksumsName, err)
		}
		if !json.Valid(data) {
			return fmt.Errorf("%s is not valid JSON", name)
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != digests[name] {
			return fmt.Errorf("%s has digest %s, %s says %s", name, got, checksumsName, digests[name])
		}
	}
	return nil
}

func TestFixturesMatchChecksums(t *testing.T) {
	if err := verifyFixtures(fixturesDir); err != nil {
		t.Fatal(err)
	}
}

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

func digestLine(name string, data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]) + "  " + name + "\n"
}

func TestVerifyFixturesRefusals(t *testing.T) {
	good := []byte("{\"a\": 1}\n")
	zero := strings.Repeat("0", 64)
	cases := []struct {
		name      string
		files     map[string]string
		wantError string // empty: the directory must verify
	}{
		{"ok", map[string]string{"a.json": string(good), "b.json": string(good), "CHECKSUMS": digestLine("a.json", good) + digestLine("b.json", good)}, ""},
		{"no CHECKSUMS", map[string]string{"a.json": string(good)}, "CHECKSUMS"},
		{"empty CHECKSUMS", map[string]string{"CHECKSUMS": ""}, "end with a line feed"},
		{"no final line feed", map[string]string{"a.json": string(good), "CHECKSUMS": strings.TrimSuffix(digestLine("a.json", good), "\n")}, "end with a line feed"},
		{"line without two spaces", map[string]string{"CHECKSUMS": zero + " a.json\n"}, "line 1"},
		{"short digest", map[string]string{"CHECKSUMS": "abc  a.json\n"}, "line 1"},
		{"upper-case digest", map[string]string{"CHECKSUMS": strings.Repeat("A", 64) + "  a.json\n"}, "line 1"},
		{"non-hex digest", map[string]string{"CHECKSUMS": strings.Repeat("g", 64) + "  a.json\n"}, "line 1"},
		{"name with a path", map[string]string{"CHECKSUMS": zero + "  sub/a.json\n"}, "not a .json file name"},
		{"name that is not json", map[string]string{"CHECKSUMS": zero + "  a.txt\n"}, "not a .json file name"},
		{"duplicate name", map[string]string{"a.json": string(good), "CHECKSUMS": digestLine("a.json", good) + digestLine("a.json", good)}, "twice"},
		{"not sorted", map[string]string{"a.json": string(good), "b.json": string(good), "CHECKSUMS": digestLine("b.json", good) + digestLine("a.json", good)}, "not sorted"},
		{"fixture not listed", map[string]string{"a.json": string(good), "b.json": string(good), "CHECKSUMS": digestLine("a.json", good)}, "b.json is not listed"},
		{"listed fixture missing", map[string]string{"CHECKSUMS": digestLine("a.json", good)}, "a.json is listed"},
		{"not JSON", map[string]string{"a.json": "{", "CHECKSUMS": digestLine("a.json", []byte("{"))}, "not valid JSON"},
		{"digest differs", map[string]string{"a.json": string(good) + " ", "CHECKSUMS": digestLine("a.json", good)}, "has digest"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err := verifyFixtures(dir)
			switch {
			case tc.wantError == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantError != "" && err == nil:
				t.Fatalf("expected an error containing %q", tc.wantError)
			case tc.wantError != "" && !strings.Contains(err.Error(), tc.wantError):
				t.Fatalf("error %q does not contain %q", err, tc.wantError)
			}
		})
	}
}
