package facade4datatug

import "context"

// IDGenerator is a PORT: the module must not import sneat-go-core,
// sneat-core-modules or another extension's backend, so anything it needs from
// outside crosses a small interface defined here and is satisfied by an
// adapter in the host composition root
// (sneat-go/pkg/modules/datatug/adapters.go). Domain tests fake the port — see
// project_create_test.go's fakeIDGenerator.
type IDGenerator interface {
	// NewID returns the id of a new record.
	NewID(ctx context.Context) (string, error)
}
