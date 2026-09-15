package datatugext

import (
	"testing"

	"github.com/sneat-co/sneat-go-core/extension"

	"github.com/datatug/backend/const4datatug"
)

// TestExtension asserts the module's identity and that it declares exactly the
// routes we expect, so a dropped or renamed route fails here rather than in
// production.
func TestExtension(t *testing.T) {
	extension.AssertExtension(t, Extension(), extension.Expected{
		ExtID:         const4datatug.ExtensionID,
		HandlersCount: 1,
	})
}
