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
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	// defaultMongoTimeout is the timeout for the context used in testing
	// whenever a context is sent to mongo
	defaultMongoTimeout = 30 * time.Second
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
	if err := db.staticSkylinks.Drop(ctx); err != nil {
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
	}
	for _, test := range tests {
		t.Run(test.name, test.test)
	}
}

// testPing is a unit test for the database's Ping method.
func testPing(t *testing.T) {
	// create context
	ctx, cancel := context.WithTimeout(context.Background(), defaultMongoTimeout)
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
	ctx, cancel := context.WithTimeout(context.Background(), defaultMongoTimeout)
	defer cancel()

	// create test database
	db := newTestDB(ctx, t.Name())
	defer db.Close()

	// Create skylink to block.
	now := time.Now().Round(time.Second).UTC()
	sl := &BlockedSkylink{
		Skylink: "somelink",
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
	fetchedSL, err := db.BlockedSkylink(ctx, sl.Skylink)
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
	ctx, cancel := context.WithTimeout(context.Background(), defaultMongoTimeout)
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
	ctx, cancel := context.WithTimeout(context.Background(), defaultMongoTimeout)
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
	ctx, cancel := context.WithTimeout(context.Background(), defaultMongoTimeout)
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
	c, err := db.staticDB.Collection(dbSkylinks).Find(db.ctx, bson.M{})
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
