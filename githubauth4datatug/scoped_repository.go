// Copyright 2026 https://datatug.io/

package githubauth4datatug

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const maxGitHubPages = 1000

func (r *AuthorizedGitHubRepository) listBranches(ctx context.Context) ([]GitHubBranch, error) {
	if err := r.requireRead(); err != nil {
		return nil, err
	}
	var branches []GitHubBranch
	for page := 1; page <= maxGitHubPages; page++ {
		var wire []struct {
			Name   string `json:"name"`
			Commit struct {
				SHA string `json:"sha"`
			} `json:"commit"`
		}
		path := fmt.Sprintf("/repos/%s/%s/branches?per_page=100&page=%d", url.PathEscape(r.repository.Owner), url.PathEscape(r.repository.Name), page)
		if err := r.getJSON(ctx, path, &wire); err != nil {
			return nil, err
		}
		for _, branch := range wire {
			if !validBranchName(branch.Name) || !validOID(branch.Commit.SHA) {
				return nil, ErrGitHubRepositoryDenied
			}
			branches = append(branches, GitHubBranch{Name: branch.Name, OID: branch.Commit.SHA})
		}
		if len(wire) < 100 {
			return branches, nil
		}
	}
	return nil, ErrGitHubRepositoryDenied
}

func (r *AuthorizedGitHubRepository) getRef(ctx context.Context, branch string) (GitHubRef, error) {
	if err := r.requireRead(); err != nil || !validBranchName(branch) {
		return GitHubRef{}, ErrGitHubRepositoryDenied
	}
	var wire struct {
		Ref    string `json:"ref"`
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	path := "/repos/" + url.PathEscape(r.repository.Owner) + "/" + url.PathEscape(r.repository.Name) + "/git/ref/heads/" + url.PathEscape(branch)
	if err := r.getJSON(ctx, path, &wire); err != nil || wire.Ref != "refs/heads/"+branch || !validOID(wire.Object.SHA) {
		return GitHubRef{}, ErrGitHubRepositoryDenied
	}
	return GitHubRef{Name: branch, OID: wire.Object.SHA}, nil
}

func (r *AuthorizedGitHubRepository) getCommit(ctx context.Context, oid string) (GitHubCommit, error) {
	if err := r.requireRead(); err != nil || !validOID(oid) {
		return GitHubCommit{}, ErrGitHubRepositoryDenied
	}
	var wire struct {
		SHA     string `json:"sha"`
		Message string `json:"message"`
		Parents []struct {
			SHA string `json:"sha"`
		} `json:"parents"`
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	path := "/repos/" + url.PathEscape(r.repository.Owner) + "/" + url.PathEscape(r.repository.Name) + "/git/commits/" + url.PathEscape(oid)
	if err := r.getJSON(ctx, path, &wire); err != nil || !validOID(wire.SHA) || !strings.EqualFold(wire.SHA, oid) || !validOID(wire.Tree.SHA) {
		return GitHubCommit{}, ErrGitHubRepositoryDenied
	}
	parents := make([]string, 0, len(wire.Parents))
	for _, parent := range wire.Parents {
		if !validOID(parent.SHA) {
			return GitHubCommit{}, ErrGitHubRepositoryDenied
		}
		parents = append(parents, parent.SHA)
	}
	return GitHubCommit{OID: wire.SHA, TreeOID: wire.Tree.SHA, Message: wire.Message, ParentOIDs: parents}, nil
}

func (r *AuthorizedGitHubRepository) getTree(ctx context.Context, rootOID string) ([]GitHubTreeEntry, error) {
	if err := r.requireRead(); err != nil || !validOID(rootOID) {
		return nil, ErrGitHubRepositoryDenied
	}
	var wire struct {
		Truncated bool `json:"truncated"`
		Tree      []struct {
			Path string `json:"path"`
			Mode string `json:"mode"`
			Type string `json:"type"`
			SHA  string `json:"sha"`
			Size int64  `json:"size"`
		} `json:"tree"`
	}
	path := "/repos/" + url.PathEscape(r.repository.Owner) + "/" + url.PathEscape(r.repository.Name) + "/git/trees/" + url.PathEscape(rootOID)
	if err := r.getJSON(ctx, path+"?recursive=1", &wire); err != nil || wire.Truncated {
		return nil, ErrGitHubRepositoryDenied
	}
	entries := make([]GitHubTreeEntry, 0, len(wire.Tree))
	for _, item := range wire.Tree {
		if !validGitHubFilePath(item.Path) || !validOID(item.SHA) || (item.Type != "tree" && item.Type != "blob" && item.Type != "commit") {
			return nil, ErrGitHubRepositoryDenied
		}
		entries = append(entries, GitHubTreeEntry{Path: item.Path, Type: item.Type, OID: item.SHA, Mode: item.Mode, Size: item.Size})
	}
	return entries, nil
}

func (r *AuthorizedGitHubRepository) getBlob(ctx context.Context, oid string) ([]byte, error) {
	if err := r.requireRead(); err != nil || !validOID(oid) {
		return nil, ErrGitHubRepositoryDenied
	}
	var wire struct {
		SHA      string `json:"sha"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	path := "/repos/" + url.PathEscape(r.repository.Owner) + "/" + url.PathEscape(r.repository.Name) + "/git/blobs/" + url.PathEscape(oid)
	if err := r.getJSON(ctx, path, &wire); err != nil || wire.SHA != oid || wire.Encoding != "base64" {
		return nil, ErrGitHubRepositoryDenied
	}
	content, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(wire.Content), ""))
	if err != nil {
		return nil, ErrGitHubRepositoryDenied
	}
	return content, nil
}

func (r *AuthorizedGitHubRepository) createCommitOnBranch(ctx context.Context, branch, expectedHeadOID, message string, changes []GitHubFileChange) (GitHubCommit, error) {
	if !validBranchName(branch) || !validOID(expectedHeadOID) || strings.TrimSpace(message) == "" || len(changes) == 0 {
		return GitHubCommit{}, ErrGitHubRepositoryDenied
	}
	additions := make([]map[string]string, 0, len(changes))
	deletions := make([]map[string]string, 0, len(changes))
	seen := make(map[string]bool, len(changes))
	for _, change := range changes {
		if !validGitHubFilePath(change.Path) || seen[change.Path] || (change.Delete && len(change.Content) != 0) {
			return GitHubCommit{}, ErrGitHubRepositoryDenied
		}
		seen[change.Path] = true
		if change.Delete {
			deletions = append(deletions, map[string]string{"path": change.Path})
		} else {
			additions = append(additions, map[string]string{"path": change.Path, "contents": base64.StdEncoding.EncodeToString(change.Content)})
		}
	}
	query := `mutation CreateCommitOnBranch($input: CreateCommitOnBranchInput!) { createCommitOnBranch(input: $input) { commit { oid message tree { oid } } } }`
	variables := map[string]any{"input": map[string]any{
		"branch":          map[string]string{"repositoryNameWithOwner": r.repository.Owner + "/" + r.repository.Name, "branchName": branch},
		"expectedHeadOid": expectedHeadOID,
		"message":         map[string]string{"headline": message},
		"fileChanges":     map[string]any{"additions": additions, "deletions": deletions},
	}}
	var response struct {
		Data struct {
			Create struct {
				Commit struct {
					OID     string `json:"oid"`
					Message string `json:"message"`
					Tree    struct {
						OID string `json:"oid"`
					} `json:"tree"`
				} `json:"commit"`
			} `json:"createCommitOnBranch"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := r.graphQL(ctx, "CreateCommitOnBranch", query, variables, &response); err != nil || len(response.Errors) != 0 || !validOID(response.Data.Create.Commit.OID) || !validOID(response.Data.Create.Commit.Tree.OID) {
		return GitHubCommit{}, ErrGitHubRepositoryDenied
	}
	return GitHubCommit{OID: response.Data.Create.Commit.OID, TreeOID: response.Data.Create.Commit.Tree.OID, Message: response.Data.Create.Commit.Message}, nil
}

func (r *AuthorizedGitHubRepository) updateRefs(ctx context.Context, updates []GitHubRefUpdate) error {
	if len(updates) == 0 {
		return ErrGitHubRepositoryDenied
	}
	refUpdates := make([]map[string]any, 0, len(updates))
	seen := make(map[string]bool, len(updates))
	for _, update := range updates {
		if !validBranchName(update.Name) || !validOID(update.BeforeOID) || !validOID(update.AfterOID) || seen[update.Name] {
			return ErrGitHubRepositoryDenied
		}
		seen[update.Name] = true
		refUpdates = append(refUpdates, map[string]any{"name": "refs/heads/" + update.Name, "beforeOid": update.BeforeOID, "afterOid": update.AfterOID, "force": false})
	}
	query := `mutation UpdateRefs($input: UpdateRefsInput!) { updateRefs(input: $input) { clientMutationId } }`
	variables := map[string]any{"input": map[string]any{"repositoryId": r.repository.NodeID, "refUpdates": refUpdates}}
	var response struct {
		Data struct {
			UpdateRefs json.RawMessage `json:"updateRefs"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := r.graphQL(ctx, "UpdateRefs", query, variables, &response); err != nil || len(response.Errors) != 0 || len(response.Data.UpdateRefs) == 0 || string(response.Data.UpdateRefs) == "null" {
		return ErrGitHubRepositoryDenied
	}
	return nil
}

func (r *AuthorizedGitHubRepository) getJSON(ctx context.Context, path string, target any) error {
	if r == nil || r.client == nil {
		return ErrGitHubRepositoryDenied
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPIURL+path, nil)
	if err != nil {
		return ErrGitHubRepositoryDenied
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	resp, err := r.client.Do(req)
	if err != nil {
		return ErrGitHubRepositoryDenied
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ErrGitHubRepositoryDenied
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, maxGitHubResponseBytes)).Decode(target); err != nil {
		return ErrGitHubRepositoryDenied
	}
	return nil
}

func (r *AuthorizedGitHubRepository) graphQL(ctx context.Context, operationName, query string, variables map[string]any, target any) error {
	if r == nil || r.client == nil {
		return ErrGitHubRepositoryDenied
	}
	body, err := json.Marshal(map[string]any{"operationName": operationName, "query": query, "variables": variables})
	if err != nil {
		return ErrGitHubRepositoryDenied
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubAPIURL+"/graphql", strings.NewReader(string(body)))
	if err != nil {
		return ErrGitHubRepositoryDenied
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	resp, err := r.client.Do(req)
	if err != nil {
		return ErrGitHubRepositoryDenied
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ErrGitHubRepositoryDenied
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, maxGitHubResponseBytes)).Decode(target); err != nil {
		return ErrGitHubRepositoryDenied
	}
	return nil
}

func (r *AuthorizedGitHubRepository) requireRead() error {
	if r == nil || r.client == nil || (r.permission != RepositoryRead && r.permission != RepositoryWrite) {
		return ErrGitHubRepositoryDenied
	}
	return nil
}

func validBranchName(branch string) bool {
	if branch == "" || strings.HasPrefix(branch, "-") || strings.HasSuffix(branch, ".") || strings.Contains(branch, "..") || strings.Contains(branch, "//") || strings.Contains(branch, "@{") {
		return false
	}
	for _, char := range branch {
		if char <= 0x20 || char == 0x7f || strings.ContainsRune("~^:?*[\\", char) {
			return false
		}
	}
	return true
}

func validOID(oid string) bool {
	if len(oid) != 40 && len(oid) != 64 {
		return false
	}
	for _, char := range oid {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return false
		}
	}
	return true
}

func validGitHubFilePath(path string) bool {
	if path == "" || strings.HasPrefix(path, "/") || strings.Contains(path, "\\") || strings.Contains(path, "//") {
		return false
	}
	for _, component := range strings.Split(path, "/") {
		if component == "" || component == "." || component == ".." {
			return false
		}
	}
	return true
}
