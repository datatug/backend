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

func TestQueryMutationPreviewRefusesUnboundedOrAmbiguousSourcePair(t *testing.T) {
	request := queryRequest()
	request.Query.ID, request.Query.Title = "customers", "Customers"
	request.Query.FolderPath = "team"
	if result, err := PreviewQueryMutation(context.Background(), request, nil); err != nil || len(result.Changes) != 2 {
		t.Fatalf("nested query pair %+v %v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := PreviewQueryMutation(ctx, request, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled operation wrote a pair: %v", err)
	}
	request.Query.FolderPath = "~"
	for _, tc := range []struct {
		name     string
		snapshot map[string][]byte
		want     error
	}{
		{"too many sidecars", map[string][]byte{"queries/customers.query.json": {}, "queries/customers.query.sql": {}, "queries/customers.query.dtql": {}, "queries/customers.query.http": {}}, ErrInvalidQuerySnapshot},
		{"oversized sidecar", map[string][]byte{"queries/customers.query.json": []byte(strings.Repeat("x", maxQueryPreviewBytes+1))}, ErrInvalidQuerySnapshot},
		{"corrupt JSON", map[string][]byte{"queries/customers.query.json": []byte(`{`)}, ErrUnsupportedExistingQuery},
		{"two metadata values", map[string][]byte{"queries/customers.query.json": []byte(`{"id":"customers","type":"DTQL"} {"id":"foreign"}`)}, ErrUnsupportedExistingQuery},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := PreviewQueryMutation(context.Background(), request, tc.snapshot); !errors.Is(err, tc.want) {
				t.Fatalf("unsafe pair accepted: %v", err)
			}
		})
	}
}

func TestPreviewQueryMutationCreatesCompletePairAndDeletesOldBody(t *testing.T) {
	request := queryRequest()
	request.Query.ID = "customers"
	request.Query.Title = "Customers"
	request.Query.ConnectionID = "chinook-sqlite"
	created, err := PreviewQueryMutation(context.Background(), request, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Changes) != 2 || created.Response.Revision == "" || created.Response.Query.FolderPath != "~" || !created.BodyChanged {
		t.Fatalf("created %+v", created)
	}
	snapshot := make(map[string][]byte)
	for _, change := range created.Changes {
		snapshot[change.Path] = change.Content
	}
	if !strings.Contains(string(snapshot["queries/customers.query.json"]), `"ovdbBaseUrl"`) || !strings.Contains(string(snapshot["queries/customers.query.json"]), `"connectionId": "chinook-sqlite"`) || strings.Contains(string(snapshot["queries/customers.query.json"]), "SELECT CustomerId") || string(snapshot["queries/customers.query.dtql"]) != request.Query.Text {
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
	if !deletedOld || !addedNew || updated.Response.Revision == created.Response.Revision || !updated.BodyChanged {
		t.Fatalf("type-change pair %+v", updated)
	}
	request.IfMatch = "stale-revision"
	if _, err := PreviewQueryMutation(context.Background(), request, snapshot); err == nil {
		t.Fatal("stale query revision accepted")
	}
}

func TestPreviewQueryMutationClassifiesBodyRatherThanMetadataOrType(t *testing.T) {
	request := queryRequest()
	request.Query.ID, request.Query.Title = "customers", "Customers"
	created, err := PreviewQueryMutation(context.Background(), request, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := make(map[string][]byte)
	for _, change := range created.Changes {
		snapshot[change.Path] = change.Content
	}
	request.IfNoneMatch = false
	request.IfMatch = created.Response.Revision
	if noOp, err := PreviewQueryMutation(context.Background(), request, snapshot); err == nil {
		t.Fatalf("no-op save produced a change candidate: %+v", noOp)
	}
	request.Query.Title = "Renamed customers"
	metadata, err := PreviewQueryMutation(context.Background(), request, snapshot)
	if err != nil || len(metadata.Changes) == 0 || metadata.BodyChanged {
		t.Fatalf("metadata-only save classified as body edit: %+v %v", metadata, err)
	}
	request.Query.Title = "Customers"
	request.Query.Type = datatug.QueryTypeSQL
	typeOnly, err := PreviewQueryMutation(context.Background(), request, snapshot)
	if err != nil || len(typeOnly.Changes) == 0 || typeOnly.BodyChanged {
		t.Fatalf("type-only save classified as body edit: %+v %v", typeOnly, err)
	}
	request.Query.Text = "SELECT 1"
	changed, err := PreviewQueryMutation(context.Background(), request, snapshot)
	if err != nil || !changed.BodyChanged {
		t.Fatalf("actual body edit omitted: %+v %v", changed, err)
	}
	request.IfNoneMatch = true
	request.IfMatch = ""
	request.Query.Text = ""
	emptyCreate, err := PreviewQueryMutation(context.Background(), request, nil)
	if err != nil || emptyCreate.BodyChanged {
		t.Fatalf("empty-body create classified as body edit: %+v %v", emptyCreate, err)
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
