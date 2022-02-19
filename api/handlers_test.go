package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"testing"
	"time"

	"github.com/SkynetLabs/blocker/database"
	"github.com/SkynetLabs/blocker/skyd"
	"github.com/sirupsen/logrus"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.mongodb.org/mongo-driver/mongo/options"
)

var (
	// v1SkylinkStr is a random skylink
	v1SkylinkStr = "BAAWi3ou51qCH24Im0ESS-5_gKg60qGIYtta-ryrl1kBnQ"
	// v2SkylinkStr is a v2 skylink that resolves to the v1 skylink
	v2SkylinkStr = "AQBst6HgaJ0PIBMtmQ2qgH_wQlFg4bNnwAhff7DmJP6oyg"
)

// mockSkyd is a helper struct that implements the Skyd API interface
type mockSkyd struct{}

// the following methods are the implementation of the interface, it's
// essentially all no-ops except for the resolver which resolves a predefined v2
// skylink to its v1.
func (api *mockSkyd) BlockHashes(hashes []string) error { return nil }
func (api *mockSkyd) IsSkydUp() bool                    { return true }
func (api *mockSkyd) ResolveSkylink(skylink skymodules.Skylink) (skymodules.Skylink, error) {
	if skylink.IsSkylinkV2() && skylink.String() == v2SkylinkStr {
		var v1 skymodules.Skylink
		if err := v1.LoadString(v1SkylinkStr); err != nil {
			panic(err)
		}
		return v1, nil
	}
	return skylink, nil
}

// mockResponseWriter is a helper struct that implements the response writer
// interface.
type mockResponseWriter struct {
	staticBuffer *bytes.Buffer
	staticHeader http.Header
}

// the following methods are the implementation of the interface, it's
// essentially all no-ops except for write, which simply writes to an internal
// buffer that can be accessed in testing
func (rw *mockResponseWriter) Header() http.Header         { return rw.staticHeader }
func (rw *mockResponseWriter) Write(b []byte) (int, error) { return rw.staticBuffer.Write(b) }
func (rw *mockResponseWriter) WriteHeader(statusCode int)  {}

// Reset is a helper function that resets the response writer, this avoids
// having to create a new one between assertions
func (rw *mockResponseWriter) Reset() {
	rw.staticBuffer.Reset()
	for k := range rw.staticHeader {
		delete(rw.staticHeader, k)
	}
}

func newMockResponseWriter() *mockResponseWriter {
	header := make(http.Header)
	return &mockResponseWriter{
		staticBuffer: bytes.NewBuffer(nil),
		staticHeader: header,
	}
}

// TestHandlers runs the handlers unit tests.
func TestHandlers(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	tests := []struct {
		name string
		test func(t *testing.T)
	}{
		{
			name: "HandleBlockRequest",
			test: testHandleBlockRequest,
		},
	}
	for _, test := range tests {
		t.Run(test.name, test.test)
	}
}

// testHandleBlockRequest verifies the functionality of the block request
// handler in the API, this method is called by both the regular and PoW block
// routes and contains all shared logic.
func testHandleBlockRequest(t *testing.T) {
	// create a context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), database.MongoDefaultTimeout)
	defer cancel()

	// create a new test API
	skyd := &mockSkyd{}
	api, err := newTestAPI("HandleBlockRequest", skyd)
	if err != nil {
		t.Fatal(err)
	}

	// create a response writer
	w := newMockResponseWriter()

	// allow list a skylink
	err = api.staticDB.CreateAllowListedSkylink(ctx, &database.AllowListedSkylink{
		Skylink:        v1SkylinkStr,
		Description:    "test skylink",
		TimestampAdded: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// create a block request for our v2 skylink
	bp := BlockPOST{
		Reporter: Reporter{
			Name:         "John",
			Email:        "john@example.com",
			OtherContact: "other@example.com",
		},
		Skylink: skylink(v2SkylinkStr),
		Tags:    []string{"tag_a", "tag_b"},
	}

	// call the request handler
	api.handleBlockRequest(context.Background(), w, bp, "")

	// assert the handler writes a 'reported' status response
	var resp statusResponse
	err = json.Unmarshal(w.staticBuffer.Bytes(), &resp)
	if err != nil {
		t.Fatal("unexpected error", err)
	}
	if resp.Status != "reported" {
		t.Fatal("unexpected response status", resp.Status)
	}

	// assert the blocked skylink did not make it into the database
	var sl skymodules.Skylink
	err = sl.LoadString(v1SkylinkStr)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := api.staticDB.FindByHash(ctx, database.NewHash(sl))
	if err != nil {
		t.Fatal("unexpected error", err)
	}
	if doc != nil {
		t.Fatal("unexpected blocked skylink found", doc)
	}

	// up until now we have asserted that the skylink gets resolved and the
	// allow list gets checked, note that this is only meaningful if the below
	// assertions pass also (happy path)

	// load a random skylink
	err = sl.LoadString("_B19BtlWtjjR7AD0DDzxYanvIhZ7cxXrva5tNNxDht1kaA")
	if err != nil {
		t.Fatal(err)
	}

	// create a block request for a random skylink
	bp = BlockPOST{
		Reporter: Reporter{
			Name:         "John",
			Email:        "john@example.com",
			OtherContact: "other@example.com",
		},
		Skylink: skylink(sl.String()),
		Tags:    []string{"tag_c", "tag_d"},
	}

	// call the request handler
	w.Reset()
	api.handleBlockRequest(context.Background(), w, bp, "")

	// assert the handler writes a 'reported' status response
	err = json.Unmarshal(w.staticBuffer.Bytes(), &resp)
	if err != nil {
		t.Fatal("unexpected error", err, string(w.staticBuffer.Bytes()))
	}
	if resp.Status != "reported" {
		t.Fatal("unexpected response status", resp.Status)
	}

	// assert the blocked skylink made it into the database
	doc, err = api.staticDB.FindByHash(ctx, database.NewHash(sl))
	if err != nil {
		t.Fatal("unexpected error", err)
	}
	if doc == nil {
		t.Fatal("expected blocked skylink to be found")
	}

	// call the request handler with the same parameters
	w.Reset()
	api.handleBlockRequest(context.Background(), w, bp, "")

	// assert the handler writes a 'duplicate' status response
	err = json.Unmarshal(w.staticBuffer.Bytes(), &resp)
	if err != nil {
		t.Fatal("unexpected error", err, string(w.staticBuffer.Bytes()))
	}
	if resp.Status != "duplicate" {
		t.Fatal("unexpected response status", resp.Status)
	}
}

// TestVerifySkappReport verifies a report directly generated from the abuse
// skapp.
func TestVerifySkappReport(t *testing.T) {
	report := `{"reporter":{"name":"PJ","email":"pj@siasky.net"},"skylink":"https://siasky.dev/_AL4LxntE4LN3WVTtvSMad3t1QGZ8c0n1bct2zfju2H_HQ","tags":["childabuse"],"pow":{"version":"MySkyID-PoW-v1","nonce":"6128653","myskyid":"a913af653d148f905f481c28fc813b6940d24e9534abceabbc0c456b0fff6cf5","signature":"d48dd2ed9227044f22aab2034973c1967722b9f50e22bf07340829a89487a764d748dc9a3640a08d7ed420a442986c24ab3fdc4cb7b959901556cf9ee87b650b"}}`

	var bp BlockWithPoWPOST
	err := json.Unmarshal([]byte(report), &bp)
	if err != nil {
		t.Fatal(err)
	}

	err = bp.PoW.Verify()
	if err != nil {
		t.Fatal(err)
	}
}

// newTestAPI returns a new API instance
func newTestAPI(dbName string, skyd skyd.API) (*API, error) {
	// create a context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), database.MongoDefaultTimeout)
	defer cancel()

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

	// purge the database
	err = db.Purge(ctx)
	if err != nil {
		panic(err)
	}

	// create the API
	api, err := New(skyd, db, logger)
	if err != nil {
		return nil, err
	}
	return api, nil
}
