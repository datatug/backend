package githubstore4datatug

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/datatug/backend/githubauth4datatug"
	"github.com/datatug/backend/template4datatug"
)

type snapshotFake struct {
	files    map[string][]byte
	tree     []githubauth4datatug.GitHubTreeEntry
	refReads int
	corrupt  bool
}

func newSnapshotFake(t *testing.T) *snapshotFake {
	t.Helper()
	files, err := template4datatug.DemoProjectFiles("demo-project-1")
	if err != nil {
		t.Fatal(err)
	}
	f := &snapshotFake{files: map[string][]byte{}}
	for name, content := range files {
		oid := testGitBlobOID(content)
		f.files[oid] = content
		f.tree = append(f.tree, githubauth4datatug.GitHubTreeEntry{Path: name, Type: "blob", OID: oid, Mode: "100644", Size: int64(len(content))})
	}
	return f
}
func (f *snapshotFake) GetRef(_ context.Context, branch string) (githubauth4datatug.GitHubRef, error) {
	f.refReads++
	return githubauth4datatug.GitHubRef{Name: branch, OID: createCommittedHead}, nil
}
func (f *snapshotFake) GetCommit(_ context.Context, oid string) (githubauth4datatug.GitHubCommit, error) {
	if oid != createCommittedHead {
		return githubauth4datatug.GitHubCommit{}, errors.New("wrong oid")
	}
	return githubauth4datatug.GitHubCommit{OID: oid, TreeOID: createTreeOID}, nil
}
func (f *snapshotFake) GetTree(_ context.Context, oid string) ([]githubauth4datatug.GitHubTreeEntry, error) {
	if oid != createTreeOID {
		return nil, errors.New("wrong tree")
	}
	return f.tree, nil
}
func (f *snapshotFake) GetBlob(_ context.Context, oid string) ([]byte, error) {
	b, ok := f.files[oid]
	if !ok {
		return nil, errors.New("wrong blob")
	}
	if f.corrupt {
		return []byte("foreign"), nil
	}
	return b, nil
}

func TestPinnedProjectSnapshotReadsRichDemoQueriesWithoutRefRefresh(t *testing.T) {
	fake := newSnapshotFake(t)
	snapshot, err := OpenProjectSnapshot(context.Background(), fake, "demo-project-1", "work")
	if err != nil {
		t.Fatal(err)
	}
	summary, err := snapshot.ProjectSummary(context.Background())
	if err != nil || !strings.Contains(string(summary), "DataTug Demo: DemoDB datasets") {
		t.Fatalf("summary %s: %v", summary, err)
	}
	folder, err := snapshot.AllQueries(context.Background())
	if err != nil || folder.ID != "~" || len(folder.Folders) != 8 {
		t.Fatalf("folders=%+v err=%v", folder, err)
	}
	for _, tc := range []struct{ folder, id, kind string }{
		{"albums", "albums_by_title", "SQL"},
		{"demodb", "chinook-top-customer-spend", "SQL"},
		{"customers", "customer-invoices", "DTQL"},
		{"reference", "country-facts", "HTTP"},
	} {
		pair, kind, err := snapshot.QueryFiles(context.Background(), tc.folder, tc.id)
		if err != nil || kind != tc.kind || len(pair) != 2 {
			t.Fatalf("%s/%s: %s %v", tc.folder, tc.id, kind, err)
		}
	}
	if fake.refReads != 1 {
		t.Fatalf("ref reads=%d, want one pinned head", fake.refReads)
	}
}

func TestPinnedProjectSnapshotRefusesBlobMismatch(t *testing.T) {
	fake := newSnapshotFake(t)
	snapshot, err := OpenProjectSnapshot(context.Background(), fake, "demo-project-1", "work")
	if err != nil {
		t.Fatal(err)
	}
	fake.corrupt = true
	if _, err = snapshot.ProjectSummary(context.Background()); !errors.Is(err, ErrInvalidProjectSnapshot) {
		t.Fatalf("corrupt blob read: %v", err)
	}
}

func TestPinnedProjectSnapshotMarksRichAndLegacyQueriesReadOnly(t *testing.T) {
	fake := newSnapshotFake(t)
	snapshot, err := OpenProjectSnapshot(context.Background(), fake, "demo-project-1", "work")
	if err != nil {
		t.Fatal(err)
	}
	for _, location := range []struct{ folder, id string }{{"albums", "albums_by_title"}, {"demodb", "chinook-top-customer-spend"}, {"customers", "customer-invoices"}} {
		read, err := snapshot.ReadQueryRevision(context.Background(), location.folder, location.id)
		if err != nil || read.SaveSupported || read.Revision == "" || read.BranchHead != createCommittedHead || !strings.Contains(string(read.Query), `"text"`) {
			t.Fatalf("%s/%s read=%+v err=%v", location.folder, location.id, read, err)
		}
	}
}

func TestPinnedProjectSnapshotReturnsCoreRevisionForNarrowSavedQuery(t *testing.T) {
	fake := newSnapshotFake(t)
	for name, content := range map[string][]byte{
		"demo-project-1/queries/saved.query.json": []byte(`{"id":"saved","title":"Saved","type":"DTQL"}`),
		"demo-project-1/queries/saved.query.dtql": []byte("SELECT CustomerId FROM chinook.Customer"),
	} {
		oid := testGitBlobOID(content)
		fake.files[oid] = content
		fake.tree = append(fake.tree, githubauth4datatug.GitHubTreeEntry{Path: name, Type: "blob", OID: oid, Mode: "100644", Size: int64(len(content))})
	}
	snapshot, err := OpenProjectSnapshot(context.Background(), fake, "demo-project-1", "work")
	if err != nil {
		t.Fatal(err)
	}
	read, err := snapshot.ReadQueryRevision(context.Background(), "~", "saved")
	if err != nil || !read.SaveSupported || len(read.Revision) != 64 || read.BranchHead != createCommittedHead {
		t.Fatalf("saved read %+v err=%v", read, err)
	}
}
