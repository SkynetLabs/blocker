package syncer

import (
	"context"
	"io/ioutil"
	"testing"
	"time"

	"github.com/SkynetLabs/blocker/blocker"
	"github.com/SkynetLabs/blocker/database"
	"github.com/SkynetLabs/blocker/skyd"
	"github.com/sirupsen/logrus"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.sia.tech/siad/crypto"
)

// mockSkyd is a helper struct that implements the skyd API, all methods are
// essentially a no-op except for 'Blocklist' which returns a hardcoded list of
// hashes and 'BlockHashes' which registers the request.
type mockSkyd struct {
	BlockHashesReqs [][]string
}

// Blocklist returns a list of hashes that make up the blocklist.
func (api *mockSkyd) Blocklist() ([]crypto.Hash, error) {
	var h1 crypto.Hash
	var h2 crypto.Hash
	h1.LoadString("44808a868caa2073e04dbae82919d0ba1f3c91d7cf121c9401cb893884aad677")
	h2.LoadString("54b34c72416b8c2ed4f9364478f209add63e9d9fd0b2065e883c8758de298440")
	return []crypto.Hash{h1, h2}, nil
}

// BlockHashes adds the given hashes to the block list.
func (api *mockSkyd) BlockHashes(hashes []string) error {
	api.BlockHashesReqs = append(api.BlockHashesReqs, hashes)
	return nil
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
	t.Run("syncDatabaseWithSkyd", testSyncDatabaseWithSkyd)
}

// testBlocklistFromPortal is a unit test for the 'blocklistFromPortal' on the
// Syncer.
func testBlocklistFromPortal(t *testing.T) {
	// create a context
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// create a test syncer
	s, err := newTestSyncer(ctx, &mockSkyd{}, "testBlocklistFromPortal")
	if err != nil {
		t.Fatal(err)
	}

	// assert the blocklist is not empty
	bl, err := s.blocklistFromPortal("https://siasky.net")
	if err != nil {
		t.Fatal(err)
	}
	if len(bl) == 0 {
		t.Fatal("expected the blocklist to contain hashes")
	}
}

// testBuildLookupTable is a small unit test that covers the functionality of
// the 'buildLookupTable' on the syncer.
func testBuildLookupTable(t *testing.T) {
	// create a context
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// create a test syncer
	s, err := newTestSyncer(ctx, &mockSkyd{}, "testBuildLookupTable")
	if err != nil {
		t.Fatal(err)
	}

	// build lookup table and assert its values
	lt, err := s.buildLookupTable()
	if err != nil {
		t.Fatal(err)
	}
	if len(lt) == 0 {
		t.Fatal("expected lookup table to not be empty")
	}

	var h1 crypto.Hash
	h1.LoadString("44808a868caa2073e04dbae82919d0ba1f3c91d7cf121c9401cb893884aad677")
	_, exists := lt[h1]
	if !exists {
		t.Fatal("expected h1 to be present in the blocklist")
	}

	var h2 crypto.Hash
	h2.LoadString("f221dfd7810b5ad0a2e4fda681591c87d30de74f3f94383bae13a265aa7948b9")
	_, exists = lt[h2]
	if exists {
		t.Fatal("expected h2 to not be present in the blocklist")
	}
}

// testSyncDatabaseWithSkyd is a unit test that verifies the functionality of
// the 'syncDatabaseWithSkyd' method on the Syncer.
func testSyncDatabaseWithSkyd(t *testing.T) {
	// create a context
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// create a mock of skyd
	skyd := &mockSkyd{}

	// create a test syncer
	s, err := newTestSyncer(ctx, skyd, "testBuildLookupTable")
	if err != nil {
		t.Fatal(err)
	}

	// create a hash
	var sl skymodules.Skylink
	sl.LoadString("_AmG887cMbafNzBnUhhzELVuiiqv5yY9AFtnOgwzCcA1Dg")
	hash := database.NewHash(sl)

	// fetch the lookup table and assert our hash is not on it
	lt, err := s.buildLookupTable()
	if err != nil {
		t.Fatal(err)
	}
	_, exists := lt[hash.Hash]
	if exists {
		t.Fatal("expected hash to not be on Skyd's blocklist")
	}

	// insert the hash into our database
	bs := &database.BlockedSkylink{
		Skylink:        sl.String(),
		Hash:           hash,
		TimestampAdded: time.Now().UTC(),
	}
	err = s.staticDB.CreateBlockedSkylink(ctx, bs)
	if err != nil {
		t.Fatal(err)
	}

	// execute a sync
	err = s.syncDatabaseWithSkyd()
	if err != nil {
		t.Fatal(err)
	}

	// assert the block request
	if len(skyd.BlockHashesReqs) != 1 {
		t.Fatalf("unexpected amount of block requests made, %v != 1", len(skyd.BlockHashesReqs))
	}
	if len(skyd.BlockHashesReqs[0]) != 1 {
		t.Fatalf("unexpected amount of hashes in block request, %v != 1", len(skyd.BlockHashesReqs[0]))
	}
	if skyd.BlockHashesReqs[0][0] != hash.String() {
		t.Fatalf("unexpected hash in block request, %v != %v", skyd.BlockHashesReqs[0][0], hash.String())
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
