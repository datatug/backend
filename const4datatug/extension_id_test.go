package const4datatug

import "testing"

func TestExtensionID(t *testing.T) {
	if ExtensionID != "datatug" {
		t.Errorf("ExtensionID = %q, want %q", ExtensionID, "datatug")
	}
}
