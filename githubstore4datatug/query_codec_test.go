package githubstore4datatug

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
)

func queryRequest() dto.SaveQueryRequest {
	return dto.SaveQueryRequest{
		ProjectRef: dto.ProjectRef{StoreID: "github.com", ProjectID: "repo@owner@datatug"},
		Branch:     "work", ExpectedBranchHead: strings.Repeat("a", 40), OperationID: "save-1", IfNoneMatch: true,
		Query: datatug.QueryDefWithFolderPath{
			FolderPath: "~",
			QueryDef: datatug.QueryDef{
				Type: datatug.QueryTypeDTQL, Text: "SELECT CustomerId FROM Customer",
				Federation: &datatug.QueryFederation{
					OVDBBaseURL: "https://demodb.dev/ovdb",
					Tables:      []datatug.QueryFederationTable{{Database: "chinook", Name: "Customer", Fields: []string{"CustomerId"}}},
				},
			},
		},
	}
}

func TestPreviewQueryMutationCreatesCompletePairAndDeletesOldBody(t *testing.T) {
	request := queryRequest()
	request.Query.ID = "customers"
	request.Query.Title = "Customers"
	created, err := PreviewQueryMutation(context.Background(), request, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Changes) != 2 || created.Response.Revision == "" || created.Response.Query.FolderPath != "~" {
		t.Fatalf("created %+v", created)
	}
	snapshot := make(map[string][]byte)
	for _, change := range created.Changes {
		snapshot[change.Path] = change.Content
	}
	if !strings.Contains(string(snapshot["queries/customers.query.json"]), `"ovdbBaseUrl"`) || strings.Contains(string(snapshot["queries/customers.query.json"]), "SELECT CustomerId") || string(snapshot["queries/customers.query.dtql"]) != request.Query.Text {
		t.Fatal("Core pair layout or metadata/body separation changed")
	}
	request.IfNoneMatch = false
	request.IfMatch = created.Response.Revision
	request.OperationID = "save-2"
	request.Query.Type = datatug.QueryTypeSQL
	request.Query.Text = "SELECT 1"
	updated, err := PreviewQueryMutation(context.Background(), request, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var deletedOld, addedNew bool
	for _, change := range updated.Changes {
		deletedOld = deletedOld || change.Path == "queries/customers.query.dtql" && change.Delete
		addedNew = addedNew || change.Path == "queries/customers.query.sql" && !change.Delete && string(change.Content) == "SELECT 1"
	}
	if !deletedOld || !addedNew || updated.Response.Revision == created.Response.Revision {
		t.Fatalf("type-change pair %+v", updated)
	}
	request.IfMatch = "stale-revision"
	if _, err := PreviewQueryMutation(context.Background(), request, snapshot); err == nil {
		t.Fatal("stale query revision accepted")
	}
}

func TestPreviewQueryMutationRejectsUnrelatedSnapshotBytes(t *testing.T) {
	request := queryRequest()
	request.Query.ID = "customers"
	request.Query.Title = "Customers"
	for _, name := range []string{"../secret", "queries/other.query.json", "queries/customers.query.json/../../secret"} {
		if _, err := PreviewQueryMutation(context.Background(), request, map[string][]byte{name: []byte("foreign")}); !errors.Is(err, ErrInvalidQuerySnapshot) {
			t.Fatalf("path %q: %v", name, err)
		}
	}
}

func TestPreviewQueryMutationRefusesLossyLegacyDefinitionUpdate(t *testing.T) {
	request := queryRequest()
	request.Query.ID = "customers"
	request.Query.Title = "Customers"
	request.IfNoneMatch = false
	request.IfMatch = "prior-revision"
	snapshot := map[string][]byte{
		"queries/customers.query.json": []byte(`{"id":"customers","title":"Customers","type":"DTQL","federation":{"ovdbBaseUrl":"https://demodb.dev/ovdb","tables":[{"database":"chinook","name":"Customer","fields":["CustomerId"]}],"expectedServerIdentity":"legacy-rich-field"}}`),
		"queries/customers.query.dtql": []byte("SELECT CustomerId FROM Customer"),
	}
	if _, err := PreviewQueryMutation(context.Background(), request, snapshot); !errors.Is(err, ErrUnsupportedExistingQuery) {
		t.Fatalf("lossy legacy definition update was accepted: %v", err)
	}
}
