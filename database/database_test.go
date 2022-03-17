package database

import (
	"context"
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
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// newTestDB creates a new database for a given test's name.
func newTestDB(dbName string) *DB {
	// create context
	ctx, cancel := context.WithTimeout(context.Background(), MongoDefaultTimeout)
	defer cancel()

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
			name: "CreateBlockedSkylink",
			test: testCreateBlockedSkylinkBulk,
		},
		{
			name: "IgnoreDuplicateKeyErrors",
			test: testIgnoreDuplicateKeyErrors,
		},
		{
			name: "IsAllowListedSkylink",
			test: testIsAllowListedSkylink,
		},
		{
			name: "MarkSucceeded",
			test: testMarkSucceeded,
		},
		{
			name: "MarkFailed",
			test: testMarkFailed,
		},
		{
			name: "MarkInvalid",
			test: testMarkInvalid,
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
	db := newTestDB(t.Name())

	// ping should succeed
	err := db.Ping(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// close it
	err = db.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// ping should fail
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
	db := newTestDB(t.Name())
	defer func() {
		err := db.Close(ctx)
		if err != nil {
			t.Fatal(err)
		}
	}()

	// verify we assert 'Hash' is set
	err := db.CreateBlockedSkylink(ctx, &BlockedSkylink{})
	if err == nil || !strings.Contains(err.Error(), "missing 'Hash' property") {
		t.Fatal("expected 'missing 'Hash' property' error", err)
	}
	err = db.CreateBlockedSkylink(ctx, &BlockedSkylink{
		Hash: HashBytes([]byte("somehash")),
	})
	if err == nil || !strings.Contains(err.Error(), "missing 'TimestampAdded' property") {
		t.Fatal("expected 'missing 'TimestampAdded' property' error", err)
	}
	err = db.CreateBlockedSkylink(ctx, &BlockedSkylink{
		Hash:           HashBytes([]byte("somehash")),
		TimestampAdded: time.Now().UTC(),
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
		Skylink:           sl.String(),
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

// testCreateBlockedSkylink tests creating blocked skylinks in bulk
func testCreateBlockedSkylinkBulk(t *testing.T) {
	// create context
	ctx, cancel := context.WithTimeout(context.Background(), MongoDefaultTimeout)
	defer cancel()

	// create test database
	db := newTestDB(t.Name())
	defer func() {
		err := db.Close(ctx)
		if err != nil {
			t.Fatal(err)
		}
	}()

	// create three blocked skylinks in bulk, make sure it contains a duplicate
	added, err := db.CreateBlockedSkylinkBulk(ctx, []BlockedSkylink{
		{
			Hash:           HashBytes([]byte("somehash1")),
			TimestampAdded: time.Now().UTC(),
		},
		{
			Hash:           HashBytes([]byte("somehash2")),
			TimestampAdded: time.Now().UTC(),
		},
		{
			Hash:           HashBytes([]byte("somehash1")),
			TimestampAdded: time.Now().UTC(),
		},
	})

	// assert there's no error and two got added
	if err != nil {
		t.Fatal(err)
	}
	if added != 2 {
		t.Fatalf("unexpected amount of skylinks blocked, %v != 2", added)
	}
}

// testIgnoreDuplicateKeyErrors is a unit test that verifies the functionality
// of ignoreDuplicateKeyErrors
func testIgnoreDuplicateKeyErrors(t *testing.T) {
	// create context
	ctx, cancel := context.WithTimeout(context.Background(), MongoDefaultTimeout)
	defer cancel()

	// create test database
	db := newTestDB(t.Name())
	defer func() {
		err := db.Close(ctx)
		if err != nil {
			t.Fatal(err)
		}
	}()

	// insert two documents with the same hash (triggers duplicate key error)
	docs := []interface{}{
		BlockedSkylink{
			Hash:           HashBytes([]byte("skylink_1")),
			TimestampAdded: time.Now().UTC(),
		},
		BlockedSkylink{
			Hash:           HashBytes([]byte("skylink_1")),
			TimestampAdded: time.Now().UTC(),
		},
	}
	_, err := db.staticSkylinks.InsertMany(ctx, docs)
	if err == nil {
		t.Fatal("unexpected nil error")
	}

	// assert the error got ignored because all write errors were duplicates
	if ignoreDuplicateKeyErrors(err) != nil {
		t.Fatal("unexpected error, should have ignored all duplicate key errs")
	}

	// cast the error to a bulk write exception and append an empty write error
	bwe, ok := err.(mongo.BulkWriteException)
	if !ok {
		t.Fatal("failed to cast error")
	}
	var custom mongo.BulkWriteError
	bwe.WriteErrors = append(bwe.WriteErrors, custom)

	// assert the error is not ignored, because it contained an unknown error
	err3 := ignoreDuplicateKeyErrors(bwe)
	if err3 == nil {
		t.Fatal("unexpected nil error, shouldn't have ignored the custom error we added")
	}
}

// testIsAllowListedSkylink tests the 'IsAllowListed' method on the database.
func testIsAllowListedSkylink(t *testing.T) {
	// create context
	ctx, cancel := context.WithTimeout(context.Background(), MongoDefaultTimeout)
	defer cancel()

	// create test database
	db := newTestDB(t.Name())
	defer func() {
		err := db.Close(ctx)
		if err != nil {
			t.Fatal(err)
		}
	}()

	// Add a skylink in the allow list
	skylink := "_B19BtlWtjjR7AD0DDzxYanvIhZ7cxXrva5tNNxDht1kaA"
	err := db.CreateAllowListedSkylink(ctx, &AllowListedSkylink{
		Skylink:        skylink,
		Description:    "test skylink",
		TimestampAdded: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Check the result of 'IsAllowListed'
	allowListed, err := db.IsAllowListed(ctx, skylink)
	if err != nil {
		t.Fatal(err)
	}
	if !allowListed {
		t.Fatal("unexpected")
	}

	// Check against a different skylink
	skylink = "ABC9BtlWtjjR7AD0DDzxYanvIhZ7cxXrva5tNNxDht1ABC"
	allowListed, err = db.IsAllowListed(ctx, skylink)
	if err != nil {
		t.Fatal(err)
	}
	if allowListed {
		t.Fatal("unexpected")
	}
}

// testMarkSucceeded is a unit test that covers the functionality of
// the 'MarkSucceeded' method on the database.
func testMarkSucceeded(t *testing.T) {
	// create context
	ctx, cancel := context.WithTimeout(context.Background(), MongoDefaultTimeout)
	defer cancel()

	// create test database
	db := newTestDB(t.Name())
	defer func() {
		err := db.Close(ctx)
		if err != nil {
			t.Fatal(err)
		}
	}()

	// ensure 'MarkSucceeded' can handle an empty slice
	var empty []Hash
	err := db.MarkSucceeded(ctx, empty)
	if err != nil {
		t.Fatal(err)
	}

	// insert a regular document and one that was marked as failed
	db.staticSkylinks.InsertOne(ctx, BlockedSkylink{
		Skylink:        "skylink_1",
		Hash:           HashBytes([]byte("skylink_1")),
		Reporter:       Reporter{},
		Tags:           []string{"tag_1"},
		TimestampAdded: time.Now().UTC(),
	})
	db.staticSkylinks.InsertOne(ctx, BlockedSkylink{
		Skylink:        "skylink_2",
		Hash:           HashBytes([]byte("skylink_2")),
		Reporter:       Reporter{},
		Tags:           []string{"tag_1"},
		TimestampAdded: time.Now().UTC(),
		Failed:         true,
	})

	toRetry, err := db.HashesToRetry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(toRetry) != 1 {
		t.Fatalf("unexpected number of documents, %v != 1", len(toRetry))
	}

	err = db.MarkSucceeded(ctx, toRetry)
	if err != nil {
		t.Fatal(err)
	}

	toRetry, err = db.HashesToRetry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(toRetry) != 0 {
		t.Fatalf("unexpected number of documents, %v != 0", len(toRetry))
	}
}

// testMarkFailed is a unit test that covers the functionality of the
// 'MarkFailed' method on the database.
func testMarkFailed(t *testing.T) {
	// create context
	ctx, cancel := context.WithTimeout(context.Background(), MongoDefaultTimeout)
	defer cancel()

	// create test database
	db := newTestDB(t.Name())
	defer func() {
		err := db.Close(ctx)
		if err != nil {
			t.Fatal(err)
		}
	}()

	// ensure 'MarkFailed' can handle an empty slice
	var empty []Hash
	err := db.MarkFailed(ctx, empty)
	if err != nil {
		t.Fatal(err)
	}

	// insert two regular documents and one invalid one
	db.CreateBlockedSkylink(ctx, &BlockedSkylink{
		Skylink:        "skylink_1",
		Hash:           HashBytes([]byte("skylink_1")),
		Reporter:       Reporter{},
		Tags:           []string{"tag_1"},
		TimestampAdded: time.Now().UTC(),
	})
	db.CreateBlockedSkylink(ctx, &BlockedSkylink{
		Skylink:        "skylink_2",
		Hash:           HashBytes([]byte("skylink_2")),
		Reporter:       Reporter{},
		Tags:           []string{"tag_1"},
		TimestampAdded: time.Now().UTC(),
	})
	db.CreateBlockedSkylink(ctx, &BlockedSkylink{
		Skylink:        "skylink_3",
		Hash:           HashBytes([]byte("skylink_3")),
		Reporter:       Reporter{},
		Tags:           []string{"tag_1"},
		TimestampAdded: time.Now().UTC(),
		Invalid:        true,
	})

	// fetch a cursor that holds all docs
	c, err := db.staticDB.Collection(collSkylinks).Find(ctx, bson.M{})
	if err != nil {
		t.Fatal(err)
	}

	// convert it to blocked skylinks
	all := make([]BlockedSkylink, 0)
	err = c.All(ctx, &all)
	if err != nil {
		t.Fatal(err)
	}

	// check we currently have 0 failed hashes
	toRetry, err := db.HashesToRetry(ctx)
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
	err = db.MarkFailed(ctx, hashes)
	if err != nil {
		t.Fatal(err)
	}

	// check we now have 2
	toRetry, err = db.HashesToRetry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(toRetry) != 2 {
		t.Fatalf("unexpected number of documents, %v != 2", len(toRetry))
	}

	// the above tests asserted that both 'HashesToRetry' and 'MarkFailed' both
	// handle invalid documents properly

	// no need to mark them as succeeded, the other unit test covers that
}

// testMarkInvalid is a unit test that covers the functionality of the
// 'MarkInvalid' method on the database.
func testMarkInvalid(t *testing.T) {
	// create a context
	ctx, cancel := context.WithTimeout(context.Background(), MongoDefaultTimeout)
	defer cancel()

	// create test database
	db := newTestDB(t.Name())
	defer db.Close(ctx)

	// ensure 'MarkInvalid' can handle an empty slice
	var empty []Hash
	err := db.MarkInvalid(ctx, empty)
	if err != nil {
		t.Fatal(err)
	}

	// insert a regular document
	hash := HashBytes([]byte("skylink_1"))
	err = db.CreateBlockedSkylink(ctx, &BlockedSkylink{
		Skylink:        "skylink_1",
		Hash:           hash,
		Reporter:       Reporter{},
		Tags:           []string{"tag_1"},
		TimestampAdded: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// assert there's one hash that needs to be blocked
	toBlock, err := db.HashesToBlock(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(toBlock) != 1 {
		t.Fatalf("expected 1 hash, instead it was %v", len(toBlock))
	}

	// assert the document is not marked as invalid
	bsl, err := db.FindByHash(ctx, hash)
	if err != nil {
		t.Fatal(err)
	}
	if bsl.Invalid {
		t.Fatal("expected invalid to be false")
	}

	// mark it as invalid
	err = db.MarkInvalid(ctx, []Hash{hash})
	if err != nil {
		t.Fatal(err)
	}

	// assert the document is marked as invalid
	bsl, err = db.FindByHash(ctx, hash)
	if err != nil {
		t.Fatal(err)
	}
	if !bsl.Invalid {
		t.Fatal("expected invalid to be true")
	}

	// assert 'HashesToBlock' excludes invalid documents
	toBlock, err = db.HashesToBlock(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(toBlock) != 0 {
		t.Fatalf("expected 0 hashes, instead it was %v", len(toBlock))
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
