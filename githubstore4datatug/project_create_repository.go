package githubstore4datatug

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/datatug/backend/facade4datatug"
	"github.com/datatug/backend/githubauth4datatug"
	"github.com/datatug/backend/template4datatug"
)

const maxCreateAncestry = 64

var ErrGitHubCreateProof = errors.New("GitHub project commit could not be proven")

type scopedCreateClient interface {
	Scope() githubauth4datatug.RepositoryScope
	GetRef(context.Context, string) (githubauth4datatug.GitHubRef, error)
	GetCommit(context.Context, string) (githubauth4datatug.GitHubCommit, error)
	GetTree(context.Context, string) ([]githubauth4datatug.GitHubTreeEntry, error)
	CreateCommitOnBranch(context.Context, string, string, string, []githubauth4datatug.GitHubFileChange) (githubauth4datatug.GitHubCommit, error)
}

type createRepository struct{ client scopedCreateClient }

var _ facade4datatug.GitHubCreateRepository = (*createRepository)(nil)

// AuthorizeCreateRepository is the production host composition seam. The
// dedicated DataTug App provider rechecks the current Firebase UID, GitHub
// actor, installation, repository identity and effective user write grant.
func AuthorizeCreateRepository(ctx context.Context, provider *githubauth4datatug.Provider, firebaseUID string, repositoryID int64, owner, name string) (facade4datatug.GitHubCreateRepository, error) {
	if provider == nil {
		return nil, githubauth4datatug.ErrGitHubAppNotConfigured
	}
	client, err := provider.AuthorizeRepository(ctx, firebaseUID, githubauth4datatug.RepositoryRef{ID: repositoryID, Owner: owner, Name: name}, githubauth4datatug.RepositoryWrite)
	if err != nil {
		return nil, err
	}
	return &createRepository{client: client}, nil
}

func (r *createRepository) Scope() facade4datatug.GitHubCreateRepositoryScope {
	if r == nil || r.client == nil {
		return facade4datatug.GitHubCreateRepositoryScope{}
	}
	scope := r.client.Scope()
	return facade4datatug.GitHubCreateRepositoryScope{
		ActorID: scope.FirebaseUID, RepositoryID: scope.Repository.ID, Owner: scope.Repository.Owner,
		Name: scope.Repository.Name, Permission: string(scope.Permission),
	}
}

func (r *createRepository) CurrentHead(ctx context.Context, branch string) (string, error) {
	ref, err := r.client.GetRef(ctx, branch)
	if err != nil || ref.Name != branch || ref.OID == "" {
		return "", ErrGitHubCreateProof
	}
	return ref.OID, nil
}

func (r *createRepository) EnsureFolderEmpty(ctx context.Context, head, folder string) error {
	commit, err := r.client.GetCommit(ctx, head)
	if err != nil || !strings.EqualFold(commit.OID, head) {
		return ErrGitHubCreateProof
	}
	tree, err := r.client.GetTree(ctx, commit.TreeOID)
	if err != nil {
		return ErrGitHubCreateProof
	}
	prefix := folder + "/"
	for _, entry := range tree {
		if entry.Path == folder || strings.HasPrefix(entry.Path, prefix) || (entry.Type != "tree" && strings.HasPrefix(folder, entry.Path+"/")) {
			return facade4datatug.ErrGitHubProjectConflict
		}
	}
	return nil
}

func (r *createRepository) CreateFilesCommit(ctx context.Context, branch, expectedHead, message string, files map[string][]byte) (string, error) {
	if len(files) == 0 || len(files) > template4datatug.DemoProjectFileCount {
		return "", ErrGitHubCreateProof
	}
	paths := make([]string, 0, len(files))
	for name := range files {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	changes := make([]githubauth4datatug.GitHubFileChange, 0, len(paths))
	for _, name := range paths {
		changes = append(changes, githubauth4datatug.GitHubFileChange{Path: name, Content: append([]byte(nil), files[name]...)})
	}
	created, err := r.client.CreateCommitOnBranch(ctx, branch, expectedHead, message, changes)
	if err != nil {
		return "", err
	}
	confirmed, err := r.client.GetCommit(ctx, created.OID)
	if err != nil || confirmed.OID != created.OID || len(confirmed.ParentOIDs) != 1 || !strings.EqualFold(confirmed.ParentOIDs[0], expectedHead) || confirmed.Message != message {
		return "", ErrGitHubCreateProof
	}
	if err := r.verifyCommitFiles(ctx, confirmed, files); err != nil {
		return "", err
	}
	return created.OID, nil
}

func (r *createRepository) FindCommitByMarker(ctx context.Context, branch, expectedHead, marker string, files map[string][]byte) (string, error) {
	head, err := r.CurrentHead(ctx, branch)
	if err != nil {
		return "", err
	}
	queue := []string{head}
	seen := make(map[string]bool)
	for len(queue) > 0 && len(seen) < maxCreateAncestry {
		oid := queue[0]
		queue = queue[1:]
		if seen[oid] || strings.EqualFold(oid, expectedHead) {
			continue
		}
		seen[oid] = true
		commit, err := r.client.GetCommit(ctx, oid)
		if err != nil || !strings.EqualFold(commit.OID, oid) {
			return "", ErrGitHubCreateProof
		}
		if commit.Message == "Create DataTug project; DataTug-Operation: "+marker {
			if len(commit.ParentOIDs) != 1 || !strings.EqualFold(commit.ParentOIDs[0], expectedHead) || r.verifyCommitFiles(ctx, commit, files) != nil {
				return "", ErrGitHubCreateProof
			}
			return commit.OID, nil
		}
		queue = append(queue, commit.ParentOIDs...)
	}
	if len(queue) > 0 {
		return "", ErrGitHubCreateProof
	}
	return "", nil
}

func (r *createRepository) verifyCommitFiles(ctx context.Context, commit githubauth4datatug.GitHubCommit, files map[string][]byte) error {
	if len(files) == 0 || len(files) > template4datatug.DemoProjectFileCount {
		return ErrGitHubCreateProof
	}
	tree, err := r.client.GetTree(ctx, commit.TreeOID)
	if err != nil {
		return ErrGitHubCreateProof
	}
	var folder string
	for name := range files {
		at := strings.IndexByte(name, '/')
		if at < 1 {
			return ErrGitHubCreateProof
		}
		if folder == "" {
			folder = name[:at]
		}
	}
	// The caller passes one selected folder. A nested folder may have a
	// common top-level prefix; inspect only exact expected paths and refuse
	// any unexpected blob beneath the selected root manifest directory.
	manifestSuffix := "/datatug-project.json"
	for name := range files {
		if strings.HasSuffix(name, manifestSuffix) {
			folder = strings.TrimSuffix(name, manifestSuffix)
			break
		}
	}
	if folder == "" {
		return ErrGitHubCreateProof
	}
	prefix := folder + "/"
	verified := make(map[string]bool, len(files))
	for _, entry := range tree {
		if !strings.HasPrefix(entry.Path, prefix) || entry.Type == "tree" {
			continue
		}
		content, expected := files[entry.Path]
		if !expected || entry.Type != "blob" || entry.Mode != "100644" || entry.Size != int64(len(content)) || !gitBlobOIDMatches(entry.OID, content) {
			return ErrGitHubCreateProof
		}
		verified[entry.Path] = true
	}
	if len(verified) != len(files) {
		return fmt.Errorf("%w: template files missing", ErrGitHubCreateProof)
	}
	return nil
}

func gitBlobOIDMatches(oid string, content []byte) bool {
	header := []byte(fmt.Sprintf("blob %d\x00", len(content)))
	if len(oid) == 40 {
		h := sha1.New()
		_, _ = h.Write(header)
		_, _ = h.Write(content)
		return strings.EqualFold(hex.EncodeToString(h.Sum(nil)), oid)
	}
	if len(oid) == 64 {
		h := sha256.New()
		_, _ = h.Write(header)
		_, _ = h.Write(content)
		return strings.EqualFold(hex.EncodeToString(h.Sum(nil)), oid)
	}
	return false
}
