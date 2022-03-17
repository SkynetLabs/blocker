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
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// create context
	ctx, cancel := context.WithTimeout(context.Background(), MongoDefaultTimeout)
	defer cancel()

	// create test database
	db := newTestDB(t.Name())
	defer db.Close(ctx)

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

// TestDiffHashes is a unit test for the DiffHashes helper method
func TestDiffHashes(t *testing.T) {
	t.Parallel()

	// create base array and two arrays we want to exclude from the diff
	hashes := []Hash{
		HashBytes([]byte("a")),
		HashBytes([]byte("b")),
		HashBytes([]byte("c")),
		HashBytes([]byte("d")),
	}
	exclude1 := []Hash{
		HashBytes([]byte("b")),
	}
	exclude2 := []Hash{
		HashBytes([]byte("d")),
		HashBytes([]byte("f")),
	}

	// diff the hashes
	diff := DiffHashes(hashes, exclude1, exclude2)
	if len(diff) != 2 {
		t.Fatalf("expected diff to contain 2 hashes, instead it was %v", len(diff))
	}

	// assert the diff
	ha := HashBytes([]byte("a")).String()
	hc := HashBytes([]byte("c")).String()

	output := ""
	for _, h := range diff {
		output += h.String()
	}
	if !(output == ha+hc || output == hc+ha) {
		t.Fatal("unexpected diff", output)
	}
}
