package database

import (
	"context"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"go.sia.tech/siad/crypto"
)

// testObject is a helper struct that contains a Hash
type testObject struct {
	Hash Hash `bson:"hash"`
}

// TestHashMarhsaling is a small unit test that verifies whether a Hash is
// properly marshaled and unmarshaled when inserted or fetched from the database
func TestHashMarhsaling(t *testing.T) {
	// create context
	ctx, cancel := context.WithTimeout(context.Background(), MongoDefaultTimeout)
	defer cancel()

	// create test database
	db := newTestDB(ctx, t.Name())
	defer db.Close()

	// create test collection and purge it
	coll := db.staticDB.Collection(t.Name())
	_, err := coll.DeleteMany(ctx, bson.M{})
	if err != nil {
		t.Fatal(err)
	}

	// insert a test object
	hash := Hash{crypto.HashBytes([]byte("helloworld"))}
	_, err = coll.InsertOne(ctx, &testObject{Hash: hash})
	if err != nil {
		t.Fatal(err)
	}

	// find the test object and decode it
	var um testObject
	err = coll.FindOne(ctx, bson.M{}).Decode(&um)
	if err != nil {
		t.Fatal(err)
	}

	// assert it's identical
	if um.Hash == (Hash{}) {
		t.Fatal("unmarshaled hash should not be empty")
	}
	if um.Hash.String() != hash.String() {
		t.Fatal("unmarshaled hash is not identical to original hash")
	}
}
