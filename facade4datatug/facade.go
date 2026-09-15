// Package facade4datatug holds the DataTug extension's application commands.
//
// It is storage-agnostic by construction: the only persistence API it uses is
// dal-go, so it never learns whether the host backs it with Firestore, an
// in-memory database (tests), or anything else. Platform needs arrive through
// ports declared in ports.go.
package facade4datatug

import "github.com/dal-go/dalgo/dal"

// Facade carries the injected database and ports for the DataTug extension's
// application commands. The host constructs one Facade at composition time
// (sneat-go/pkg/modules/datatug), wiring a real adapter per port; tests
// construct it over an in-memory database with fakes.
type Facade struct {
	db          dal.DB
	ids         IDGenerator
	githubOAuth GithubOAuthExchanger
}

// NewFacade returns a Facade over the given database and ports.
func NewFacade(db dal.DB, ids IDGenerator, githubOAuth GithubOAuthExchanger) Facade {
	return Facade{db: db, ids: ids, githubOAuth: githubOAuth}
}
