package const4datatug

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExtensionID(t *testing.T) {
	assert.Equal(t, "datatug", string(ExtensionID))
}
