package syncer

import (
	"context"
	"crypto/rand"
	"io/ioutil"
	"sync"
	"testing"
	"time"

	"github.com/SkynetLabs/blocker/blocker"
	"github.com/SkynetLabs/blocker/database"
	"github.com/SkynetLabs/blocker/skyd"
	"github.com/sirupsen/logrus"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.sia.tech/siad/build"
	"go.sia.tech/siad/crypto"
)

// mockSkyd is a helper struct that implements the skyd API, all methods are
// essentially a no-op except for 'Blocklist' which returns a hardcoded list of
// hashes and 'BlockHashes' which registers the request.
type mockSkyd struct {
	blockHashesReqs [][]database.Hash
	mu              sync.Mutex
}

// Blocklist returns a list of hashes that make up the blocklist.
func (api *mockSkyd) Blocklist() ([]crypto.Hash, error) {
	return []crypto.Hash{randomHash()}, nil
}

// BlockHashes adds the given hashes to the block list.
func (api *mockSkyd) BlockHashes(hashes []database.Hash) ([]database.Hash, []database.Hash, error) {
	api.mu.Lock()
	defer api.mu.Unlock()
	api.blockHashesReqs = append(api.blockHashesReqs, hashes)
	return hashes, nil, nil
}

// IsSkydUp returns true if the skyd API instance is up.
func (api *mockSkyd) IsSkydUp() bool { return true }

// ResolveSkylink tries to resolve the given skylink to a V1 skylink.
func (api *mockSkyd) ResolveSkylink(skylink skymodules.Skylink) (skymodules.Skylink, error) {
	return skylink, nil
}

// TestSyncer is a collection of unit tests to verify the functionality of the
// Syncer.
func TestSyncer(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	t.Run("blocklistFromPortal", testBlocklistFromPortal)
	t.Run("buildLookupTable", testBuildLookupTable)
	t.Run("randomHash", testRandomHash)
	t.Run("start", testStart)
	t.Run("syncBlocklistWithSkyd", testSyncBlocklistWithSkyd)
}

// testBlocklistFromPortal is an integration test that fetches the blocklist
// from siasky.net
func testBlocklistFromPortal(t *testing.T) {
	// create a context
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// create a mock of skyd
	skyd := &mockSkyd{}

	// create a test syncer
	s, err := newTestSyncer(ctx, skyd, "testSyncBlocklistWithSkyd")
	if err != nil {
		t.Fatal(err)
	}

	// fetch the blocklist and assert it's not empty
	blocklist, err := s.blocklistFromPortal("https://siasky.net")
	if err != nil {
		t.Fatal("unexpected error occurred fetching blocklist from siasky.net", err)
	}
	if len(blocklist) == 0 {
		t.Fatal("expected blocklist to be non empty", len(blocklist))
	}
}

// testBuildLookupTable is a small unit test that covers the functionality of
// the 'buildLookupTable' helper.
func testBuildLookupTable(t *testing.T) {
	// create three random hashes
	h1 := randomHash()
	h2 := randomHash()
	h3 := randomHash()

	// base case
	lt := buildLookupTable(nil)
	if len(lt) != 0 {
		t.Fatal("expected lookup table to not be empty")
	}

	// rebuild a lookup table with two hashes
	lt = buildLookupTable([]crypto.Hash{h1, h2})

	// assert the first two hashes exists and the third one does not
	_, exists := lt[h1]
	if !exists {
		t.Fatal("expected lookup table to contain h1")
	}
	_, exists = lt[h2]
	if !exists {
		t.Fatal("expected lookup table to contain h2")
	}
	_, exists = lt[h3]
	if exists {
		t.Fatal("expected lookup table to not contain h3")
	}
}

// testRandomHash is a small unit test for the randomHash helper
func testRandomHash(t *testing.T) {
	var empty crypto.Hash
	if empty.String() == randomHash().String() {
		t.Fatal("expected random hash to be different from an empty hash")
	}
}

// testStart is an integration test that syncs siasky.net's blocklist with our
// mock skyd instance
func testStart(t *testing.T) {
	// create a context
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// create a mock of skyd
	skyd := &mockSkyd{}

	// create a test syncer
	s, err := newTestSyncer(ctx, skyd, "testSyncBlocklistWithSkyd")
	if err != nil {
		t.Fatal(err)
	}

	s.staticPortalURLs = []string{"https://siasky.net"}

	// start the syncer
	s.Start()

	// check in a loop whether we've synced at least once
	err = build.Retry(100, 100*time.Millisecond, func() error {
		skyd.mu.Lock()
		numRequests := len(skyd.blockHashesReqs)
		skyd.mu.Unlock()
		if numRequests > 0 {
			return nil
		}
		return errors.New("no block requests received yet")
	})
	if err != nil {
		t.Fatal("unexpected error", err)
	}

	// assert there's at least one request with 'batchsize' hashes
	skyd.mu.Lock()
	defer skyd.mu.Unlock()
	requests := skyd.blockHashesReqs
	if len(requests[0]) != blocker.BlockBatchSize {
		t.Fatal("expected at least one block request that submitted 'batchsize' hashes")
	}
}

// testSyncBlocklistWithSkyd is a unit test for the 'syncBlocklistWithSkyd' on
// the Syncer.
func testSyncBlocklistWithSkyd(t *testing.T) {
	// create a context
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// create a mock of skyd
	skyd := &mockSkyd{}

	// create a test syncer
	s, err := newTestSyncer(ctx, skyd, "testSyncBlocklistWithSkyd")
	if err != nil {
		t.Fatal(err)
	}

	// fetch skyd's blocklist
	hashes, err := skyd.Blocklist()
	if err != nil {
		t.Fatal(err)
	}

	// build the existing lookup table
	existing := buildLookupTable(hashes)

	// fake an update which contains one unknown hash
	hash := randomHash()
	hashes = append(hashes, hash)
	update := buildLookupTable(hashes)

	// sync the diff with skyd
	added, err := s.syncBlocklistWithSkyd(existing, update)
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 {
		t.Fatal("expected one hash to get added")
	}

	// assert the received block request in skyd
	if len(skyd.blockHashesReqs) != 1 {
		t.Fatal("expected one call to skyd's block endpoint")
	}
	if len(skyd.blockHashesReqs[0]) != 1 {
		t.Fatal("expected one hash to be added")
	}
	if skyd.blockHashesReqs[0][0].String() != hash.String() {
		t.Fatalf("expected %v to get added, but instead it was %v", hash.String(), skyd.blockHashesReqs[0][0].String())
	}
}

// newTestSyncer returns a test syncer object.
func newTestSyncer(ctx context.Context, skydAPI skyd.API, dbName string) (*Syncer, error) {
	// create a nil logger
	logger := logrus.New()
	logger.Out = ioutil.Discard

	// create database
	db, err := database.NewCustomDB(ctx, "mongodb://localhost:37017", dbName, options.Credential{
		Username: "admin",
		Password: "aO4tV5tC1oU3oQ7u",
	}, logger)
	if err != nil {
		return nil, err
	}

	// purge it
	err = db.Purge(ctx)
	if err != nil {
		return nil, err
	}

	// create the blocker
	b, err := blocker.New(ctx, skydAPI, db, logger)
	if err != nil {
		return nil, err
	}

	// create a syncer
	return New(ctx, b, skydAPI, db, nil, logger)
}

// randomHash returns a random hash
func randomHash() crypto.Hash {
	var h crypto.Hash
	rand.Read(h[:])
	return h
}
