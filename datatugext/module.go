// Package datatug is the DataTug backend module.
//
// It is composed into the Sneat platform host (sneat-go) as an extension: the
// host calls Extension() and registers the routes it declares. Domain and
// persistence logic live in the api4datatug / facade4datatug / dbo4datatug
// packages; the host only wires and configures.
package datatugext

import (
	"github.com/sneat-co/sneat-go-core/extension"

	"github.com/datatug/backend/api4datatug"
	"github.com/datatug/backend/const4datatug"
)

// Extension returns the DataTug extension config: its module identity plus the
// HTTP routes to mount.
func Extension() extension.Config {
	return extension.NewExtension(
		const4datatug.ExtensionID,
		extension.RegisterRoutes(api4datatug.RegisterHttpRoutes),
	)
}
