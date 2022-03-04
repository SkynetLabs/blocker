package database

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.sia.tech/siad/crypto"
)

// TestDatabase runs the database unit tests.
func TestDatabase(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	tests := []struct {
		name string
		test func(t *testing.T)
	}{
		{
			name: "Ping",
			test: testPing,
		},
		{
			name: "CreateBlockedSkylink",
			test: testCreateBlockedSkylink,
		},
		{
			name: "IsAllowListedSkylink",
			test: testIsAllowListedSkylink,
		},
		{
			name: "MarkAsSucceeded",
			test: testMarkAsSucceeded,
		},
		{
			name: "MarkAsFailed",
			test: testMarkAsFailed,
		},
		{
			name: "HasIndex",
			test: testHasIndex,
		},
		{
			name: "DropIndex",
			test: testDropIndex,
		},
	}
	for _, test := range tests {
		t.Run(test.name, test.test)
	}
}

// testPing is a unit test for the database's Ping method.
func testPing(t *testing.T) {
	// create context
	ctx, cancel := context.WithTimeout(context.Background(), MongoDefaultTimeout)
	defer cancel()

	// create test database
	db := newTestDB(ctx, t.Name())
	defer db.Close()
	err := db.Ping(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = db.staticClient.Disconnect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Ping(ctx)
	if err == nil {
		t.Fatal("should fail")
	}
}

// testCreateBlockedSkylink tests creating and fetching a blocked skylink from
// the db.
func testCreateBlockedSkylink(t *testing.T) {
	// create context
	ctx, cancel := context.WithTimeout(context.Background(), MongoDefaultTimeout)
	defer cancel()

	// create test database
	db := newTestDB(ctx, t.Name())
	defer db.Close()

	// verify we assert 'Hash' is set
	err := db.CreateBlockedSkylink(ctx, &BlockedSkylink{})
	if err == nil || !strings.Contains(err.Error(), "'hash' is not set") {
		t.Fatal("expected 'hash is not set' error", err)
	}
	err = db.CreateBlockedSkylink(ctx, &BlockedSkylink{
		Hash: HashBytes([]byte("somehash")),
	})
	if err != nil {
		t.Fatal("unexpected error", err)
	}

	// create skylink to block.
	var sl skymodules.Skylink
	err = sl.LoadString("_B19BtlWtjjR7AD0DDzxYanvIhZ7cxXrva5tNNxDht1kaA")
	if err != nil {
		t.Fatal("unexpected error", err)
	}
	hash := NewHash(sl)

	// create a blocked skylink struct
	now := time.Now().Round(time.Second).UTC()
	bsl := &BlockedSkylink{
		Hash: hash,
		Reporter: Reporter{
			Name:            "name",
			Email:           "email",
			OtherContact:    "other",
			Sub:             "sub",
			Unauthenticated: true,
		},
		Reverted:          true,
		RevertedTags:      []string{"A"},
		Tags:              []string{"B"},
		TimestampAdded:    now,
		TimestampReverted: now.AddDate(1, 1, 1),
	}
	err = db.CreateBlockedSkylink(ctx, bsl)
	if err != nil {
		t.Fatal(err)
	}

	// Fetch it again.
	fetchedSL, err := db.FindByHash(ctx, hash)
	if err != nil {
		t.Fatal(err)
	}
	if fetchedSL == nil {
		t.Fatal("should have found the skylink")
	}

	// Set the id of the fetchedSL on the sl.
	bsl.ID = fetchedSL.ID

	// Compare.
	if !reflect.DeepEqual(*bsl, *fetchedSL) {
		b1, _ := json.Marshal(*bsl)
		b2, _ := json.Marshal(*fetchedSL)
		fmt.Println(string(b1))
		fmt.Println(string(b2))
		t.Fatal("not equal")
	}
}

// testIsAllowListedSkylink tests the 'IsAllowListed' method on the database.
func testIsAllowListedSkylink(t *testing.T) {
	// create context
	ctx, cancel := context.WithTimeout(context.Background(), MongoDefaultTimeout)
	defer cancel()

	// create test database
	db := newTestDB(ctx, t.Name())
	defer db.Close()

	// Add a skylink in the allow list
	hash := randomHash()
	err := db.CreateAllowListedSkylink(ctx, &AllowListedSkylink{
		Hash:           Hash{hash},
		Description:    "test skylink",
		TimestampAdded: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Check the result of 'IsAllowListed'
	allowListed, err := db.IsAllowListed(ctx, hash)
	if err != nil {
		t.Fatal(err)
	}
	if !allowListed {
		t.Fatal("unexpected")
	}

	// Check against a different skylink
	hash2 := randomHash()
	allowListed, err = db.IsAllowListed(ctx, hash2)
	if err != nil {
		t.Fatal(err)
	}
	if allowListed {
		t.Fatal("unexpected")
	}
}

// testMarkAsSucceeded is a unit test that covers the functionality of
// the 'MarkAsSucceeded' method on the database.
func testMarkAsSucceeded(t *testing.T) {
	// create context
	ctx, cancel := context.WithTimeout(context.Background(), MongoDefaultTimeout)
	defer cancel()

	// create test database
	db := newTestDB(ctx, t.Name())
	defer db.Close()

	// ensure 'MarkAsSucceeded' can handle an empty slice
	var empty []Hash
	err := db.MarkAsSucceeded(empty)
	if err != nil {
		t.Fatal(err)
	}

	// insert a regular document and one that was marked as failed
	err = db.CreateBlockedSkylink(ctx, &BlockedSkylink{
		Hash:           HashBytes([]byte("skylink_1")),
		Reporter:       Reporter{},
		Tags:           []string{"tag_1"},
		TimestampAdded: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.CreateBlockedSkylink(ctx, &BlockedSkylink{
		Hash:           HashBytes([]byte("skylink_2")),
		Reporter:       Reporter{},
		Tags:           []string{"tag_1"},
		TimestampAdded: time.Now().UTC(),
		Failed:         true,
	})
	if err != nil {
		t.Fatal(err)
	}

	toRetry, err := db.HashesToRetry()
	if err != nil {
		t.Fatal(err)
	}
	if len(toRetry) != 1 {
		t.Fatalf("unexpected number of documents, %v != 1", len(toRetry))
	}

	err = db.MarkAsSucceeded(toRetry)
	if err != nil {
		t.Fatal(err)
	}

	toRetry, err = db.HashesToRetry()
	if err != nil {
		t.Fatal(err)
	}
	if len(toRetry) != 0 {
		t.Fatalf("unexpected number of documents, %v != 0", len(toRetry))
	}
}

// testMarkAsFailed is a unit test that covers the functionality of
// the 'MarkAsFailed' method on the database.
func testMarkAsFailed(t *testing.T) {
	// create context
	ctx, cancel := context.WithTimeout(context.Background(), MongoDefaultTimeout)
	defer cancel()

	// create test database
	db := newTestDB(ctx, t.Name())
	defer db.Close()

	// ensure 'MarkAsFailed' can handle an empty slice
	var empty []Hash
	err := db.MarkAsFailed(empty)
	if err != nil {
		t.Fatal(err)
	}

	// insert two regular documents
	db.CreateBlockedSkylink(ctx, &BlockedSkylink{
		Hash:           HashBytes([]byte("skylink_1")),
		Reporter:       Reporter{},
		Tags:           []string{"tag_1"},
		TimestampAdded: time.Now().UTC(),
	})
	db.CreateBlockedSkylink(ctx, &BlockedSkylink{
		Hash:           HashBytes([]byte("skylink_2")),
		Reporter:       Reporter{},
		Tags:           []string{"tag_1"},
		TimestampAdded: time.Now().UTC(),
	})

	// fetch a cursor that holds all docs
	c, err := db.staticDB.Collection(collSkylinks).Find(db.ctx, bson.M{})
	if err != nil {
		t.Fatal(err)
	}

	// convert it to blocked skylinks
	all := make([]BlockedSkylink, 0)
	err = c.All(db.ctx, &all)
	if err != nil {
		t.Fatal(err)
	}

	// check we currently have 0 failed hashes
	toRetry, err := db.HashesToRetry()
	if err != nil {
		t.Fatal(err)
	}
	if len(toRetry) != 0 {
		t.Fatalf("unexpected number of documents, %v != 0", len(toRetry))
	}

	// mark all hashes as failed
	hashes := make([]Hash, len(all))
	for i, doc := range all {
		hashes[i] = doc.Hash
	}
	err = db.MarkAsFailed(hashes)
	if err != nil {
		t.Fatal(err)
	}

	// check we now have 2
	toRetry, err = db.HashesToRetry()
	if err != nil {
		t.Fatal(err)
	}
	if len(toRetry) != 2 {
		t.Fatalf("unexpected number of documents, %v != 2", len(toRetry))
	}

	// no need to mark them as succeeded, the other unit test covers that
}

// testHasIndex is a unit test that verifies the functionality of the hasIndex
// helper function
func testHasIndex(t *testing.T) {
	// create context
	ctx, cancel := context.WithTimeout(context.Background(), MongoDefaultTimeout)
	defer cancel()

	// create test database
	db := newTestDB(ctx, t.Name())
	defer db.Close()

	// check whether we can find an index we expect to be there
	found, err := hasIndex(ctx, db.staticSkylinks, "hash")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("unexpected")
	}

	// check whether the output is correct for a made up index name
	found, err = hasIndex(ctx, db.staticSkylinks, "nonexistingindexname")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("unexpected")
	}
}

// testDropIndex is a unit test that verifies the functionality of the dropIndex
// helper function
func testDropIndex(t *testing.T) {
	// create context
	ctx, cancel := context.WithTimeout(context.Background(), MongoDefaultTimeout)
	defer cancel()

	// create test database
	db := newTestDB(ctx, t.Name())
	defer db.Close()

	// check whether dropIndex errors out on an unknown index
	dropped, err := dropIndex(ctx, db.staticSkylinks, "nonexistingindexname")
	if err != nil {
		t.Fatal(err)
	}
	if dropped {
		t.Fatal("unexpected")
	}

	// check the output for an existing index
	dropped, err = dropIndex(ctx, db.staticSkylinks, "hash")
	if err != nil {
		t.Fatal(err)
	}
	if !dropped {
		t.Fatal("unexpected")
	}
}

// define a helper function to decode a skylink as string into a skylink obj
func skylinkFromString(skylink string) (sl skymodules.Skylink) {
	err := sl.LoadString(skylink)
	if err != nil {
		panic(err)
	}
	return
}

// newTestDB creates a new database for a given test's name.
func newTestDB(ctx context.Context, dbName string) *DB {
	dbName = strings.ReplaceAll(dbName, "/", "-")
	logger := logrus.New()
	logger.Out = ioutil.Discard
	db, err := NewCustomDB(ctx, "mongodb://localhost:37017", dbName, options.Credential{
		Username: "admin",
		Password: "aO4tV5tC1oU3oQ7u",
	}, logger)
	if err != nil {
		panic(err)
	}
	err = db.Purge(ctx)
	if err != nil {
		panic(err)
	}
	return db
}

// randomHash returns a random hash
func randomHash() crypto.Hash {
	var h crypto.Hash
	rand.Read(h[:])
	return h
}
