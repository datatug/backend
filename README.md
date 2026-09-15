# DataTug backend

Go domain module for the DataTug extension. Module path:
`github.com/datatug/backend`.

Built to the org standard
[`extension-backend-architecture.md`](https://github.com/sneat-co/sneat-specs/blob/main/standards/extension-backend-architecture.md):
the module depends on **`dal-go` only** — never `sneat-go-core`,
`sneat-core-modules`, or another extension's backend. Anything it needs from
outside the domain crosses a **port** defined here and is satisfied by an
**adapter** in the host composition root.

`github.com/sneat-co/sneat-go-core` appears in `go.mod` **for tests only**:
`sneatcoretesting.NewMemoryDB()` gives the domain tests a real in-memory
dal-go database (it also enforces Firestore's transaction rules), so no
Firestore emulator or platform bootstrapping is needed.

## What's here

| Package | What it is |
| --- | --- |
| `const4datatug` | Extension id (`datatug`) |
| `models4datatug` | DBOs and dalgo key builders: the project record `datatug_projects/{projectID}` and the user's DataTug index `users/{userID}/ext/datatug` |
| `facade4datatug` | `Facade` (injected `dal.DB` + ports) and the `CreateProject` command; `ports.go` holds the `IDGenerator` port |

## `CreateProject`

`Facade.CreateProject(ctx, userID, storeID, title)` creates a project in the
DataTug cloud store, in one transaction:

1. reads the user's DataTug index **first** — Firestore requires every read in
   a transaction to happen before the first write;
2. inserts `datatug_projects/{projectID}` — title, private access, the creating
   user, `created.at`;
3. registers the project brief in the user's index: inserting
   `users/{userID}/ext/datatug` when the user has none, and otherwise updating
   only the new project's field path, so two concurrent creates cannot clobber
   each other's briefs.

Only the cloud store (`storeID == "firestore"`) is served. A project in a
**GitHub repo** is created by the client that holds the user's GitHub
credential (`datatug-apps`, with a user PAT), and a **local filesystem**
project by the `datatug` CLI agent.

Extension-owned user data lives in the user-ext document
(`users/{uid}/ext/{extID}`), never inline in the core `users/{uid}` document.
The module builds that key itself (`models4datatug.NewUserExtKey`) rather than
importing `sneat-core-modules`' `dal4userus`, to keep the dal-go-only rule; a
test pins the exact path.

## Host wiring

| Concern | Where it lives |
| --- | --- |
| HTTP endpoint | `sneat-go/pkg/modules/datatug/api4datatug` — `POST /v0/datatug/projects/create_project?store=firestore` |
| Ports | `sneat-go/pkg/modules/datatug/adapters.go` (`IDGenerator`) |
| Extension registration | `sneat-go/pkg/modules/datatug/module.go`, composed in `pkg/sneatmain/sneat_main.go`'s `extraModules` |

## Build & test

```bash
go build ./...
go test ./...
go vet ./...
```

## CI & versioning

`.github/workflows/backend-ci.yml` runs strongo's Standard Go CI (lint · test ·
build) on pushes and PRs touching Go sources, and auto-tags the next `vX.Y.Z` on
push to `main` from conventional-commit messages.
