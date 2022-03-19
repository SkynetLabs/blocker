package blocker

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SkynetLabs/blocker/api"
	"github.com/SkynetLabs/blocker/database"
	"github.com/sirupsen/logrus"
	skyapi "gitlab.com/SkynetLabs/skyd/node/api"
)

// mockBlocklistResponse is a mock handler for the /skynet/blocklist endpoint
func mockBlocklistResponse(w http.ResponseWriter, r *http.Request) {
	var request skyapi.SkynetBlocklistPOST
	err := json.NewDecoder(r.Body).Decode(&request)
	if err != nil {
		panic(err)
	}

	var invalids []api.InvalidInput
	invalidHashStr := database.HashBytes([]byte("invalid_hash")).String()
	for _, hash := range request.Add {
		if hash == invalidHashStr {
			invalids = append(invalids, api.InvalidInput{Input: hash, Error: "invalid hash"})
		}
	}

	var response api.BlockResponse
	response.Invalids = invalids
	skyapi.WriteJSON(w, response)
}

// TestBlocker runs the blocker unit tests
func TestBlocker(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// create a test server that returns mocked responses used by our subtests
	mux := http.NewServeMux()
	mux.HandleFunc("/skynet/blocklist", mockBlocklistResponse)
	server := httptest.NewServer(mux)
	defer server.Close()

	tests := []struct {
		name string
		test func(t *testing.T, s *httptest.Server)
	}{
		{
			name: "BlockHashes",
			test: testBlockHashes,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.test(t, server)
		})
	}
}

// testBlockHashes is a unit test that covers the 'blockHashes' method.
func testBlockHashes(t *testing.T, server *httptest.Server) {
	// create a client that connects to our server
	client := api.NewSkydClient(server.URL, "")

	// create the blocker
	ctx, cancel := context.WithCancel(context.Background())
	blocker, err := newTestBlocker(ctx, "BlockHashes", client)
	if err != nil {
		t.Fatal(err)
	}

	// start the syncer
	err = blocker.Start()
	if err != nil {
		t.Fatal(err)
	}

	// defer a call to stops
	defer func() {
		cancel()
		err := blocker.Stop()
		if err != nil {
			t.Fatal(err)
		}
	}()

	// create a list of 16 hashes, where the 10th hash is one that triggers an
	// error to be thrown in skyd, this will ensure the blocker tries:
	// - all hashes in 1 batch
	// - a batch size of 10, which still fails
	// - all hashes in a batch size of 1, which returns the failing hash
	var hashes []database.Hash
	var i int
	for ; i < 9; i++ {
		hash := database.HashBytes([]byte(fmt.Sprintf("skylink_hash_%d", i)))
		hashes = append(hashes, hash)
	}

	// the last hash before the failure should be the latest timestamp set,
	// so save this timestamp as an expected value for later
	hashes = append(hashes, database.HashBytes([]byte("invalid_hash")))
	for ; i < 15; i++ {
		hash := database.HashBytes([]byte(fmt.Sprintf("skylink_hash_%d", i)))
		hashes = append(hashes, hash)
	}

	blocked, invalid, err := blocker.BlockHashes(hashes)
	if err != nil {
		t.Fatal("unexpected error thrown", err)
	}
	// assert blocked and failed are returned correctly
	if blocked != 15 {
		t.Errorf("unexpected return values for blocked, %v != 15", blocked)
	}
	if invalid != 1 {
		t.Fatalf("unexpected return values for invalid, %v != 1", invalid)
	}
}

// newTestBlocker returns a new blocker instance
func newTestBlocker(ctx context.Context, dbName string, skydClient *api.SkydClient) (*Blocker, error) {
	// create a nil logger
	logger := logrus.New()
	logger.Out = ioutil.Discard

	// create database
	db := database.NewTestDB(context.Background(), dbName, logger)

	// create the blocker
	blocker, err := New(skydClient, db, logger)
	if err != nil {
		return nil, err
	}
	return blocker, nil
}
