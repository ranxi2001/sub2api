package adminaccess

import "testing"

func TestGroupProtectionRequiresGlobalReadAndNeverInheritsModelScope(t *testing.T) {
	path := "/api/v1/admin/groups/:id/anti-distillation"
	for _, permission := range []string{"groups.models.read", "groups.models.write", "users.read"} {
		if RouteAllowed(Policy{GlobalPermissions: []string{permission}, Rules: []Rule{{Permissions: []string{permission}, GroupIDs: []int64{7}}}}, "GET", path) {
			t.Fatalf("%s must not grant access to protection settings", permission)
		}
	}
	if !RouteAllowed(Policy{GlobalPermissions: []string{"groups.read"}}, "GET", path) {
		t.Fatal("global group readers must be able to read protection settings")
	}
	if RouteAllowed(Policy{GlobalPermissions: []string{"groups.read"}}, "PUT", path) {
		t.Fatal("read permission must not grant protection writes")
	}
}
