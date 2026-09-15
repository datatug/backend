// Package datatugext exposes the DataTug extension config for the platform host
// to compose. The host supplies the adapter for the domain's IDGenerator port,
// which keeps the domain free of any platform or third-party id library.
package datatugext

import (
	"github.com/datatug/backend/api4datatug"
	"github.com/datatug/backend/const4datatug"
	"github.com/datatug/backend/facade4datatug"
	"github.com/sneat-co/sneat-go-core/extension"
)

// Extension returns the DataTug extension config: its module identity, its HTTP
// routes, and the host-supplied IDGenerator adapter.
func Extension(ids facade4datatug.IDGenerator) extension.Config {
	return extension.NewExtension(
		const4datatug.ExtensionID,
		extension.RegisterRoutes(func(handle extension.HTTPHandleFunc) {
			api4datatug.RegisterHttpRoutes(handle, ids)
		}),
	)
}
