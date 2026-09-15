# DataTug backend

The DataTug cloud backend: a [Sneat](https://sneat.co) platform extension that
`sneat-go` (the `api.sneat.cloud` host) composes through `Extension()`.

## Endpoints

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/v0/datatug/projects/create_project?store=firestore` | Create a DataTug project in the user's cloud store |

Request (auth required — `Authorization: Bearer <firebase-id-token>`):

```json
{ "title": "My project" }
```

Response `201 Created`:

```json
{ "id": "<projectID>" }
```

The store is read from the `?store=` query parameter, exactly as the
`datatug` CLI agent's `create_project` endpoint does, so the web client can
reuse one call shape. Only `firestore` (the DataTug cloud store) is served
here — see "Not served here" below.

## What creating a project does

In a single dal-go transaction (`facade.RunReadwriteTransaction`):

1. inserts `datatug_projects/{projectID}` — `{title, access, userIDs, created.at}`;
2. registers the project brief in the user's DataTug index,
   `users/{userID}/ext/datatug`: inserting that document when the user has none,
   and otherwise updating only the new project's field path, so two concurrent
   creates cannot clobber each other's briefs.

## Design rules

- **The facade does not know where data is stored.** `facade4datatug` uses only
  dal-go (`dal.DB` / `dal.ReadwriteTransaction`, through
  `sneat-go-core/facade`) and imports no database client, so it runs against any
  dal-go backed database. The host supplies Firestore via `facade.GetSneatDB`.
- **Extension-owned user data lives in the user-ext document**
  (`users/{uid}/ext/{extID}`, see `dal4userus.NewUserExtKey`), never inline in
  the core `users/{uid}` document.
- **Route paths are literal.** The host mounts them verbatim; no module-id or
  `/v0/` prefix is added.

## Not served here

- A project in a **GitHub repo** is created by the client that holds the user's
  GitHub credential (`datatug-apps`, with a user-supplied PAT).
- A project on a **local filesystem** is created by the `datatug` CLI agent.

This API is the cloud (Firestore) store only.

## Development

```bash
go build ./...
go vet ./...
go test ./... -coverprofile=cover.out
```
