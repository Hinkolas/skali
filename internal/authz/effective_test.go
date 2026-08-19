package authz

import "testing"

func TestEffective(t *testing.T) {
	cell := func(r Role) *Role { return &r }
	cases := []struct {
		name          string
		instanceAdmin bool
		member        bool
		projectRole   Role
		cell          *Role
		maxRole       Role
		want          Role
	}{
		{"instance admin wins over everything", true, false, None, cell(None), None, Admin},
		{"cell overrides the project role downwards", false, true, Maintain, cell(Read), Admin, Read},
		{"cell overrides the project role upwards", false, true, Read, cell(Deploy), Admin, Deploy},
		{"cell locks", false, true, Maintain, cell(None), Admin, None},
		{"cell is not capped by the ceiling", false, true, Read, cell(Maintain), Read, Maintain},
		{"project admin is never capped", false, true, Admin, nil, Read, Admin},
		{"project role below the ceiling passes through", false, true, Deploy, nil, Maintain, Deploy},
		{"project role above the ceiling is capped", false, true, Maintain, nil, Read, Read},
		{"ceiling none locks every inheriting member", false, true, Maintain, nil, None, None},
		{"non-member without cell gets none", false, false, None, nil, Admin, None},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Effective(tc.instanceAdmin, tc.member, tc.projectRole, tc.cell, tc.maxRole)
			if got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestParseRole(t *testing.T) {
	for _, name := range RoleNames() {
		r, ok := ParseRole(name)
		if !ok || r.String() != name {
			t.Fatalf("round trip %q failed: %v %v", name, r, ok)
		}
	}
	if _, ok := ParseRole("owner"); ok {
		t.Fatal("owner parsed")
	}
	if ValidProjectRole("none") {
		t.Fatal("none is not a project role")
	}
	if !ValidCellRole("none") || !ValidMaxRole("none") {
		t.Fatal("none is a cell and ceiling role")
	}
	if !Admin.AtLeast(Maintain) || Read.AtLeast(Deploy) {
		t.Fatal("ordering broken")
	}
}
