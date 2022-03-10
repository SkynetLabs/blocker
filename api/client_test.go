package api

import "testing"

// TestClient contains subtests for the client and makes up the testing suite
func TestClient(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	tests := []struct {
		name string
		test func(t *testing.T)
	}{
		{
			name: "BlocklistGET",
			test: testBlocklistGET,
		},
	}
	for _, test := range tests {
		t.Run(test.name, test.test)
	}
}

// testBlocklistGET ensures the client can fetch the blocklist
func testBlocklistGET(t *testing.T) {
	t.Parallel()

	c := NewClient("https://siasky.dev")
	bl, err := c.BlocklistGET(0)
	if err != nil {
		t.Fatal(err)
	}

	if len(bl.Entries) == 0 {
		t.Fatal("expected at least one entry")
	}
}
