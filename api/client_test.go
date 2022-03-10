package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	skyapi "gitlab.com/SkynetLabs/skyd/node/api"
)

// mockPortalBlocklistResponse is a mock handler for the
// /skynet/portal/blocklist endpoint
func mockPortalBlocklistResponse(w http.ResponseWriter, r *http.Request) {
	var blg BlocklistGET
	blg.Entries = append(blg.Entries, BlockedHash{})
	skyapi.WriteJSON(w, blg)
}

// TestClient contains subtests for the client and makes up the testing suite
func TestClient(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// create a test server that returns mocked responses used by our subtests
	mux := http.NewServeMux()
	mux.HandleFunc("/skynet/portal/blocklist", mockPortalBlocklistResponse)
	server := httptest.NewServer(mux)
	defer server.Close()

	tests := []struct {
		name string
		test func(t *testing.T, s *httptest.Server)
	}{
		{
			name: "BlocklistGET",
			test: testBlocklistGET,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) { test.test(t, server) })
	}
}

// testBlocklistGET ensures the client can fetch the blocklist
func testBlocklistGET(t *testing.T, s *httptest.Server) {
	c := NewClient(s.URL)
	bl, err := c.BlocklistGET(0)
	if err != nil {
		t.Fatal(err)
	}

	if len(bl.Entries) == 0 {
		t.Fatal("expected at least one entry")
	}
}
