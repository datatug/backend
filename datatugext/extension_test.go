package datatugext

import (
	"context"
	"testing"

	"github.com/datatug/backend/const4datatug"
	"github.com/sneat-co/sneat-go-core/extension"
)

type fakeIDs struct{}

func (fakeIDs) NewID(context.Context) (string, error) { return "proj1234", nil }

// TestExtension asserts the module's identity and that it declares exactly the
// routes we expect, so a dropped or renamed route fails here rather than in
// production.
func TestExtension(t *testing.T) {
	extension.AssertExtension(t, Extension(fakeIDs{}), extension.Expected{
		ExtID:         const4datatug.ExtensionID,
		HandlersCount: 1,
	})
}
