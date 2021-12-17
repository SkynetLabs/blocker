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
	"go.mongodb.org/mongo-driver/mongo/options"
)

// newTestDB creates a new database for a given test's name.
func newTestDB(dbName string) *DB {
	dbName = strings.ReplaceAll(dbName, "/", "-")
	logger := logrus.New()
	logger.Out = ioutil.Discard
	db, err := NewCustomDB(context.Background(), "mongodb://localhost:37017", dbName, options.Credential{
		Username: "admin",
		Password: "aO4tV5tC1oU3oQ7u",
	}, logger)
	if err != nil {
		panic(err)
	}
	if err := db.staticSkylinks.Drop(context.Background()); err != nil {
		panic(err)
	}
	return db
}

// TestDatabase runs the database unit tests.
func TestDatabase(t *testing.T) {
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
	}
	for _, test := range tests {
		t.Run(test.name, test.test)
	}
}

// testPing is a unit test for the database's Ping method.
func testPing(t *testing.T) {
	db := newTestDB(t.Name())
	defer db.Close()

	err := db.Ping(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = db.staticClient.Disconnect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = db.Ping(context.Background())
	if err == nil {
		t.Fatal("should fail")
	}
}

// testCreateBlockedSkylink tests creating and fetching a blocked skylink from
// the db.
func testCreateBlockedSkylink(t *testing.T) {
	db := newTestDB(t.Name())
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
	err := db.CreateBlockedSkylink(context.Background(), sl)
	if err != nil {
		t.Fatal(err)
	}

	// Fetch it again.
	fetchedSL, err := db.BlockedSkylink(context.Background(), sl.Skylink)
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
