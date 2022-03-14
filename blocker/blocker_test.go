package blocker

import (
	"context"
	"fmt"
	"io/ioutil"
	"testing"

	"github.com/SkynetLabs/blocker/database"
	"github.com/SkynetLabs/blocker/skyd"
	"github.com/sirupsen/logrus"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// mockSkyd is a helper struct that implements the skyd API, all methods are
// essentially a no-op except for 'BlockHashes' which keeps track of the
// arguments with which it is called
type mockSkyd struct {
	blockHashesReqs [][]database.Hash
}

// BlockHashes adds the given hashes to the block list.
func (api *mockSkyd) BlockHashes(hashes []database.Hash) ([]database.Hash, []database.Hash, error) {
	api.blockHashesReqs = append(api.blockHashesReqs, hashes)

	// filter out "invalid" hashes
	var invalids []database.Hash
	for _, h := range hashes {
		if h.String() == database.HashBytes([]byte("invalid_hash")).String() {
			invalids = append(invalids, h)
		}
	}

	// return the valid hashes, invalid hashes and no error
	return database.DiffHashes(hashes, invalids), invalids, nil
}

// IsSkydUp returns true if the skyd API instance is up.
func (api *mockSkyd) IsSkydUp() bool {
	return true
}

// ResolveSkylink tries to resolve the given skylink to a V1 skylink.
func (api *mockSkyd) ResolveSkylink(skylink skymodules.Skylink) (skymodules.Skylink, error) {
	return skylink, nil
}

// TestBlocker runs the blocker unit tests
func TestBlocker(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	tests := []struct {
		name string
		test func(t *testing.T)
	}{
		{
			name: "BlockHashes",
			test: testBlockHashes,
		},
	}
	for _, test := range tests {
		t.Run(test.name, test.test)
	}
}

// testBlockHashes is a unit test that covers the 'blockHashes' method.
func testBlockHashes(t *testing.T) {
	// create a mock skyd api
	api := &mockSkyd{}

	// create the blocker
	ctx, cancel := context.WithCancel(context.Background())
	blocker, err := newTestBlocker(ctx, "BlockHashes", api)
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

	// assert only 1 request happened to the block endpoint
	if len(api.blockHashesReqs) != 1 {
		t.Fatalf("unexpected amount of block requests, %v != 1", len(api.blockHashesReqs))
	}
	// assert that request contained all hashes
	if len(api.blockHashesReqs[0]) != 16 {
		t.Fatalf("unexpected amount of hashes, %v != 16", len(api.blockHashesReqs[0]))
	}
}

// newTestBlocker returns a new blocker instance
func newTestBlocker(ctx context.Context, dbName string, api skyd.API) (*Blocker, error) {
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

	// create the blocker
	blocker, err := New(ctx, api, db, logger)
	if err != nil {
		return nil, err
	}
	return blocker, nil
}
