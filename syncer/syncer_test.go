package syncer

import (
	"context"
	"crypto/rand"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SkynetLabs/blocker/api"
	"github.com/SkynetLabs/blocker/database"
	"github.com/sirupsen/logrus"
	"gitlab.com/NebulousLabs/errors"
	skyapi "gitlab.com/SkynetLabs/skyd/node/api"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.sia.tech/siad/build"
	"go.sia.tech/siad/crypto"
)

// TestSyncer is a collection of unit tests to verify the functionality of the
// Syncer.
func TestSyncer(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	t.Run("lastSyncedHash", testLastSyncedHash)
	t.Run("randomHash", testRandomHash)
	t.Run("syncer", testSyncer)
}

// testLastSyncedHash is a unit test that verifies the last synced hash setter
// and getter on the Syncer.
func testLastSyncedHash(t *testing.T) {
	t.Parallel()

	// create a test syncer
	s, err := newTestSyncer("testLastSyncedHash", nil)
	if err != nil {
		t.Fatal(err)
	}

	// basic case
	portalURL := "https://siasky.net"
	lastSynced := s.managedLastSyncedHash(portalURL)
	if lastSynced != "" {
		t.Fatal("unexpected", lastSynced)
	}

	// update and check
	hash := randomHash()
	s.managedUpdateLastSyncedHash(portalURL, hash.String())
	lastSynced = s.managedLastSyncedHash(portalURL)
	if lastSynced != hash.String() {
		t.Fatal("unexpected", lastSynced)
	}
}

// testRandomHash is a small unit test for the randomHash helper
func testRandomHash(t *testing.T) {
	var empty crypto.Hash
	if empty.String() == randomHash().String() {
		t.Fatal("expected random hash to be different from an empty hash")
	}
}

type mockAPI struct{}

func (api mockAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	panic("wtf")
}

// testSyncer is an integration test that syncs siasky.net's blocklist with our
// mock skyd instance
func testSyncer(t *testing.T) {
	// create a mocked blocklist response returning two hashes
	hash1 := randomHash()
	hash2 := randomHash()
	blg := api.BlocklistGET{
		Entries: []api.BlockedHash{
			{Hash: hash1, Tags: []string{"tag_1"}},
			{Hash: hash2, Tags: []string{"tag_2"}},
		},
		HasMore: false,
	}

	// create a small server that returns our response
	mux := http.NewServeMux()
	mux.HandleFunc("/portal/blocklist", func(w http.ResponseWriter, r *http.Request) {
		skyapi.WriteJSON(w, blg)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	// create a test syncer that syncs from our server
	s, err := newTestSyncer("testSyncer", []string{server.URL})
	if err != nil {
		t.Fatal(err)
	}

	// insert one hash manually, this will assert that our insert ignores
	// duplicate entries
	s.staticDB.CreateBlockedSkylink(context.Background(), &database.BlockedSkylink{
		Hash:           database.Hash{hash1},
		TimestampAdded: time.Now().UTC(),
	})

	// assert the database contains our one entry
	hashes, _, err := s.staticDB.BlockedHashes(1, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(hashes) != 1 {
		t.Fatalf("unexpected number of blocked hashes, %v != 1", len(hashes))
	}

	// start the syncer
	err = s.Start()
	if err != nil {
		t.Fatal(err)
	}

	// check in a loop whether we're filling up the database
	err = build.Retry(100, 100*time.Millisecond, func() error {
		hashes, _, err := s.staticDB.BlockedHashes(1, 0, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(hashes) == 1 {
			return errors.New("no new hashes yet")
		}
		return nil
	})
	if err != nil {
		t.Fatal("unexpected error", err)
	}

	// fetch hashes to block, we expect to see two
	toBlock, err := s.staticDB.HashesToBlock(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(toBlock) != 2 {
		t.Fatalf("unexpected number of hashes to block, %v != 2", len(toBlock))
	}

	// assert the second one is our hash that got synced
	if toBlock[1].String() != hash2.String() {
		t.Fatalf("unexpected hash %v != %v", toBlock[1].String(), hash2.String())
	}

	// fetch the entire database entry
	bsl, err := s.staticDB.FindByHash(context.Background(), toBlock[1])
	if err != nil {
		t.Fatal(err)
	}

	// asser the reporter is properly filled
	if bsl.Reporter.Name != server.URL {
		t.Fatalf("unexpected reporter '%v'", bsl.Reporter.Name)
	}

	// assert the tags are filled
	if len(bsl.Tags) != 1 {
		t.Fatalf("unexpected number of tags, %v != 1", len(bsl.Tags))
	}
	if bsl.Tags[0] != "tag_2" {
		t.Fatalf("unexpected tag, %v != tag_2", bsl.Tags[0])
	}
}

// newTestSyncer returns a test syncer object.
func newTestSyncer(dbName string, portalURLs []string) (*Syncer, error) {
	// create a nil logger
	logger := logrus.New()
	logger.Out = ioutil.Discard

	// create database
	db, err := database.NewCustomDB(context.Background(), "mongodb://localhost:37017", dbName, options.Credential{
		Username: "admin",
		Password: "aO4tV5tC1oU3oQ7u",
	}, logger)
	if err != nil {
		return nil, err
	}

	// Define a new context with a timeout to handle the database setup.
	ctx, cancel := context.WithTimeout(context.Background(), database.MongoDefaultTimeout)
	defer cancel()

	// purge it
	err = db.Purge(ctx)
	if err != nil {
		return nil, err
	}

	// create a syncer
	return New(context.Background(), db, portalURLs, logger)
}

// randomHash returns a random hash
func randomHash() crypto.Hash {
	var h crypto.Hash
	rand.Read(h[:])
	return h
}
