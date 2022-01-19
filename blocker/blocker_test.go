package blocker

import (
	"context"
	"fmt"
	"io/ioutil"
	"testing"

	"github.com/SkynetLabs/blocker/database"
	"github.com/SkynetLabs/blocker/skyd"
	"github.com/sirupsen/logrus"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.sia.tech/siad/crypto"
)

// mockSkyd is a helper struct that implements the skyd API, all methods are
// essentially a no-op except for 'BlockHashes' which keeps track of the
// arguments with which it is called
type mockSkyd struct {
	BlockHashesReqs [][]string
}

// BlockHashes adds the given hashes to the block list.
func (api *mockSkyd) BlockHashes(hashes []string) error {
	api.BlockHashesReqs = append(api.BlockHashesReqs, hashes)

	// check whether the caller expects an error to be thrown
	for _, h := range hashes {
		if h == crypto.HashBytes([]byte("throwerror")).String() {
			return errors.New(unableToUpdateBlocklistErrStr)
		}
	}
	return nil
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
	blocker, err := newTestBlocker("BlockHashes", api)
	if err != nil {
		panic(err)
	}

	// defer a db close
	defer func() {
		if err := blocker.staticDB.Close(); err != nil {
			t.Error(err)
		}
	}()

	// create a list of 16 hashes, where the 10th hash is one that triggers an
	// error to be thrown in skyd, this will ensure the blocker tries:
	// - all hashes in 1 batch
	// - a batch size of 10, which still fails
	// - all hashes in a batch size of 1, which returns the failing hash
	var hashes []crypto.Hash
	var i int
	for ; i < 9; i++ {
		hash := crypto.HashBytes([]byte(fmt.Sprintf("skylink_hash_%d", i)))
		hashes = append(hashes, hash)
	}

	// the last hash before the failure should be the latest timestamp set,
	// so save this timestamp as an expected value for later
	hashes = append(hashes, crypto.HashBytes([]byte("throwerror")))
	for ; i < 15; i++ {
		hash := crypto.HashBytes([]byte(fmt.Sprintf("skylink_hash_%d", i)))
		hashes = append(hashes, hash)
	}

	blocked, failed, err := blocker.blockHashes(hashes)
	if err != nil {
		t.Fatal("unexpected error thrown", err)
	}
	// assert blocked and failed are returned correctly
	if blocked != 15 {
		t.Fatalf("unexpected return values for blocked, %v != 15", blocked)
	}
	if failed != 1 {
		t.Fatalf("unexpected return values for failed, %v != 1", failed)
	}

	// assert 18 requests in total happen to skyd, batch size 100, 10 and 1
	if len(api.BlockHashesReqs) != 18 {
		t.Fatalf("unexpected amount of calls to Skyd block endpoint, %v != 18", len(api.BlockHashesReqs))
	}
	if len(api.BlockHashesReqs[0]) != 16 {
		t.Fatalf("unexpected first batch size, %v != 16", len(api.BlockHashesReqs[0]))
	}
	if len(api.BlockHashesReqs[1]) != 10 {
		t.Fatalf("unexpected second batch size, %v != 10", len(api.BlockHashesReqs[1]))
	}
	for r := 2; r < 18; r++ {
		if len(api.BlockHashesReqs[r]) != 1 {
			t.Fatalf("unexpected batch size for req %d, %v != 1", r, len(api.BlockHashesReqs[r]))
		}
	}
}

// newTestBlocker returns a new blocker instance
func newTestBlocker(dbName string, api skyd.API) (*Blocker, error) {
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

	// create the blocker
	blocker, err := New(context.Background(), api, db, logger)
	if err != nil {
		return nil, err
	}
	return blocker, nil
}
