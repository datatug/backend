package facade4datatug

import (
	"context"
	"time"

	"github.com/dal-go/dalgo/dal"
)

// IDGenerator is a PORT: extension implementation packages stay outside this
// module. Published contracts and their DTO aliases may be reused; behavior
// crosses a small interface defined here and is satisfied by an
// adapter in the host composition root
// (sneat-go/pkg/modules/datatug/adapters.go). Domain tests fake the port — see
// facade_test.go's fakeIDGenerator.
type IDGenerator interface {
	// NewID returns the id of a new record.
	NewID(ctx context.Context) (string, error)
}

// SharedProjectCreateBinding contains only immutable values. ActorID must come
// from the verified request context, never an HTTP body. A trusted adapter must
// independently bind it to that context when preparing Space authority.
type SharedProjectCreateBinding struct {
	ActorID, SpaceID, CommandID, RequestDigest string
	// Paid binding is server-resolved. Legacy ownership-only commands leave it empty.
	PayerID, Mode, Product string
}

// SharedProjectCreateAuthority is supplied by the host using released Core
// Space contracts: existing DataTug capability, current ordinary active Space,
// and server-configured content-management roles. It must not enable a module.
// Core reservation labels have a narrower grammar than client command IDs:
// the adapter must derive a server-owned lowercase command digest (<=64 chars)
// and purpose/stage labels, not pass arbitrary client CommandID through Core.
type SharedProjectCreateAuthority interface {
	PrepareSharedProjectCreate(context.Context, SharedProjectCreateBinding) (PreparedSharedProjectCreate, error)
}

// PreparedSharedProjectCreate is server-to-server only. Its validator privately
// owns the opaque Core reservation; that reservation never enters this DTO or
// a public response. The domain checks Binding and captures its validation
// clock AFTER preparation, before entering any retryable callback.
type PreparedSharedProjectCreate struct {
	Binding   SharedProjectCreateBinding
	IssuedAt  time.Time
	Validator SharedProjectCreateTransactionAuthority
}

type SharedProjectCreateTransactionAuthority interface {
	// ValidateSharedProjectCreateInTransaction must re-read current Space,
	// membership/role, enabled module and durable reservation evidence in this
	// exact transaction on EVERY invocation, including command replay. It may
	// only read: no nested transaction, writes, provider call or clock read.
	ValidateSharedProjectCreateInTransaction(context.Context, dal.ReadwriteTransaction, SharedProjectCreateBinding, time.Time) error
}
