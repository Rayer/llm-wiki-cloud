package v1

import "testing"

func TestDefaultProjectIdentityDoesNotUseDisplayName(t *testing.T) {
	if canRecoverProjectFromOwnerSuffix("owner_other", "owner", "default") {
		t.Fatal("a different owner-prefixed suffix was treated as the default Project")
	}
	if !canRecoverProjectFromOwnerSuffix("owner_default", "owner", "default") {
		t.Fatal("the exact owner-prefixed stable ID suffix was not recoverable")
	}
	if _, _, ok := projectResponseFromFirestoreDoc("owner_My First Wiki", map[string]interface{}{
		"user_id": "owner", "project_id": "default", "name": "My First Wiki",
	}); !ok {
		t.Fatal("a display-label suffix with an explicit stable Project ID was not parsed")
	}
}
