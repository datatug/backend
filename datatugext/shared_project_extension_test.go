package datatugext

import (
	"testing"

	"github.com/datatug/backend/api4datatug"
	"github.com/datatug/backend/const4datatug"
	"github.com/sneat-co/sneat-go-core/extension"
)

func TestSharedProjectExtensionOptionsAreAdditive(t *testing.T) {
	extension.AssertExtension(t, ExtensionWithOptions(fakeIDs{}, api4datatug.RouteOptions{}), extension.Expected{ExtID: const4datatug.ExtensionID, HandlersCount: 13})
}
