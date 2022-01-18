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
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.sia.tech/siad/crypto"
)

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
	_, err = db.staticSkylinks.DeleteMany(ctx, bson.D{})
	if err != nil {
		panic(err)
	}
	_, err = db.staticAllowList.DeleteMany(ctx, bson.D{})
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
			name: "compatTransformSkylinkToHash",
			test: testCompatTransformSkylinkToHash,
		},
	}
	for _, test := range tests {
		t.Run(test.name, test.test)
	}
}

// testPing is a unit test for the database's Ping method.
func testPing(t *testing.T) {
	// create context
	ctx, cancel := context.WithTimeout(context.Background(), mongoDefaultTimeout)
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
	ctx, cancel := context.WithTimeout(context.Background(), mongoDefaultTimeout)
	defer cancel()

	// create test database
	db := newTestDB(ctx, t.Name())
	defer db.Close()

	// Create skylink to block.
	now := time.Now().Round(time.Second).UTC()
	sl := &BlockedSkylink{
		Skylink: "somelink",
		Hash:    crypto.HashBytes([]byte("somelink")),
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
	err := db.CreateBlockedSkylink(ctx, sl)
	if err != nil {
		t.Fatal(err)
	}

	// Fetch it again.
	fetchedSL, err := db.FindBySkylink(ctx, sl.Skylink)
	if err != nil {
		t.Fatal(err)
	}

	// Set the id of the fetchedSL on the sl.
	sl.ID = fetchedSL.ID

	// Compare.
	if !reflect.DeepEqual(*sl, *fetchedSL) {
		b1, _ := json.Marshal(*sl)
		b2, _ := json.Marshal(*fetchedSL)
		fmt.Println(string(b1))
		fmt.Println(string(b2))
		t.Fatal("not equal")
	}
}

// testIsAllowListedSkylink tests the 'IsAllowListed' method on the database.
func testIsAllowListedSkylink(t *testing.T) {
	// create context
	ctx, cancel := context.WithTimeout(context.Background(), mongoDefaultTimeout)
	defer cancel()

	// create test database
	db := newTestDB(ctx, t.Name())
	defer db.Close()

	// Add a skylink in the allow list
	skylink := "_B19BtlWtjjR7AD0DDzxYanvIhZ7cxXrva5tNNxDht1kaA"
	_, err := db.staticAllowList.InsertOne(ctx, &AllowListedSkylink{
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

// testMarkAsSucceeded is a unit test that covers the functionality of
// the 'MarkAsSucceeded' method on the database.
func testMarkAsSucceeded(t *testing.T) {
	// create context
	ctx, cancel := context.WithTimeout(context.Background(), mongoDefaultTimeout)
	defer cancel()

	// create test database
	db := newTestDB(ctx, t.Name())
	defer db.Close()

	// insert a regular document and one that was marked as failed
	db.staticSkylinks.InsertOne(ctx, BlockedSkylink{
		Skylink:        "skylink_1",
		Reporter:       Reporter{},
		Tags:           []string{"tag_1"},
		TimestampAdded: time.Now().UTC(),
	})
	db.staticSkylinks.InsertOne(ctx, BlockedSkylink{
		Skylink:        "skylink_2",
		Reporter:       Reporter{},
		Tags:           []string{"tag_1"},
		TimestampAdded: time.Now().UTC(),
		Failed:         true,
	})

	toRetry, err := db.SkylinksToRetry()
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

	toRetry, err = db.SkylinksToRetry()
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
	ctx, cancel := context.WithTimeout(context.Background(), mongoDefaultTimeout)
	defer cancel()

	// create test database
	db := newTestDB(ctx, t.Name())
	defer db.Close()

	// insert two regular documents
	db.staticSkylinks.InsertOne(ctx, BlockedSkylink{
		Skylink:        "skylink_1",
		Reporter:       Reporter{},
		Tags:           []string{"tag_1"},
		TimestampAdded: time.Now().UTC(),
	})
	db.staticSkylinks.InsertOne(ctx, BlockedSkylink{
		Skylink:        "skylink_2",
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

	// check we currently have 0 failed skylinks
	toRetry, err := db.SkylinksToRetry()
	if err != nil {
		t.Fatal(err)
	}
	if len(toRetry) != 0 {
		t.Fatalf("unexpected number of documents, %v != 0", len(toRetry))
	}

	// mark all docs as failed
	err = db.MarkAsFailed(all)
	if err != nil {
		t.Fatal(err)
	}

	// check we now have 2
	toRetry, err = db.SkylinksToRetry()
	if err != nil {
		t.Fatal(err)
	}
	if len(toRetry) != 2 {
		t.Fatalf("unexpected number of documents, %v != 2", len(toRetry))
	}

	// no need to mark them as succeeded, the other unit test covers that
}

// testCompatTransformSkylinkToHash is a unit test that verifies the correct
// execution of the 'compatTransformSkylinkToHash' method on the database.
func testCompatTransformSkylinkToHash(t *testing.T) {
	// create context
	ctx, cancel := context.WithTimeout(context.Background(), mongoDefaultTimeout)
	defer cancel()

	// create test database
	db := newTestDB(ctx, t.Name())
	defer db.Close()

	// define a helper function to count the number of documents that the compat
	// code should run on
	countOldDocs := func() int {
		// find all documents where the skyink has to be transformed to a hash
		docs, err := db.find(ctx, bson.D{{"$and", []interface{}{
			bson.D{{"skylink", bson.M{"$exists": true}}},
			bson.D{{"$or", []interface{}{
				bson.D{{"hash", nil}},
				bson.D{{"hash", bson.M{"$exists": false}}},
				bson.D{{"hash", emptyHash}},
			}}},
		}}})
		if err != nil {
			t.Fatal(err)
		}
		return len(docs)
	}

	// define a helper function to decode a skylink as string into a skylink obj
	skylinkFromString := func(skylink string) (sl skymodules.Skylink) {
		err := sl.LoadString(skylink)
		if err != nil {
			t.Fatal(err)
		}
		return
	}

	// assert the number of 'old' docs is 0
	numOld := countOldDocs()
	if numOld != 0 {
		t.Fatalf("unexpected amount of docs, %v != 0", numOld)
	}

	// run the compat code on an empty database
	err := db.compatTransformSkylinkToHash(ctx)
	if err != nil {
		t.Fatal("unexpected error", err)
	}

	// prepare 3 skylinks
	sl1 := skylinkFromString("BAAWi3ou51qCH24Im0ESS-5_gKg60qGIYtta-ryrl1kBnQ")
	sl2 := skylinkFromString("IABst6HgaJ0PIBMtmQ2qgH_wQlFg4bNnwAhff7DmJP6oyg")
	sl3 := skylinkFromString("IABXaRBvjDTB3RizX3RfwdCoxt2Tff1buEXhlO7b9Unn8g")

	// insert two documents with a skylink but no hash (like it was originally)
	db.staticSkylinks.InsertOne(ctx, BlockedSkylink{
		Skylink:        sl1.String(),
		Reporter:       Reporter{},
		Tags:           []string{"tag_1"},
		TimestampAdded: time.Now().UTC(),
	})
	db.staticSkylinks.InsertOne(ctx, BlockedSkylink{
		Skylink:        sl2.String(),
		Reporter:       Reporter{},
		Tags:           []string{"tag_1"},
		TimestampAdded: time.Now().Add(time.Minute).UTC(),
	})

	// insert one documents with a skylink and a hash
	db.staticSkylinks.InsertOne(ctx, BlockedSkylink{
		Skylink:        sl3.String(),
		Hash:           crypto.Hash(sl3.MerkleRoot()),
		Reporter:       Reporter{},
		Tags:           []string{"tag_1"},
		TimestampAdded: time.Now().Add(time.Hour).UTC(),
	})

	// assert the number of 'old' docs is 2
	numOld = countOldDocs()
	if numOld != 2 {
		t.Fatalf("unexpected amount of docs, %v != 2", numOld)
	}

	// run the compat code again
	err = db.compatTransformSkylinkToHash(ctx)
	if err != nil {
		t.Fatal("unexpected error", err)
	}

	// find all skylink documents, in order
	opts := options.Find()
	opts.SetSort(bson.D{{"timestamp_added", 1}})
	skylinks, err := db.find(ctx, bson.D{}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(skylinks) != 3 {
		t.Fatalf("unexpected amount of docs, %v != 3", len(skylinks))
	}

	// assert the skylink and hash value for all documents
	sl11 := skylinkFromString(skylinks[0].Skylink)
	if sl11.String() != sl1.String() {
		t.Fatal("unexpected skylink value")
	}
	if crypto.Hash(sl11.MerkleRoot()) != skylinks[0].Hash {
		t.Fatal("unexpected hash value", crypto.Hash(sl11.MerkleRoot()), skylinks[0].Hash)
	}
	sl22 := skylinkFromString(skylinks[1].Skylink)
	if sl22.String() != sl2.String() {
		t.Fatal("unexpected skylink value")
	}
	if crypto.Hash(sl22.MerkleRoot()) != skylinks[1].Hash {
		t.Fatal("unexpected hash value")
	}
	sl33 := skylinkFromString(skylinks[2].Skylink)
	if sl33.String() != sl3.String() {
		t.Fatal("unexpected skylink value")
	}
	if crypto.Hash(sl33.MerkleRoot()) != skylinks[2].Hash {
		t.Fatal("unexpected hash value")
	}
}
