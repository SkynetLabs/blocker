package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	url "net/url"
	"strings"
	"testing"
	"time"

	"github.com/SkynetLabs/blocker/database"
	skyapi "gitlab.com/SkynetLabs/skyd/node/api"
	"gitlab.com/SkynetLabs/skyd/skymodules"
)

var (
	// v1SkylinkStr is a random skylink
	v1SkylinkStr = "BAAWi3ou51qCH24Im0ESS-5_gKg60qGIYtta-ryrl1kBnQ"
	// v2SkylinkStr is a v2 skylink that resolves to the v1 skylink
	v2SkylinkStr = "AQBst6HgaJ0PIBMtmQ2qgH_wQlFg4bNnwAhff7DmJP6oyg"
)

// mockResponseWriter is a helper struct that implements the response writer
// interface.
type mockResponseWriter struct {
	staticBuffer *bytes.Buffer
	staticHeader http.Header
}

// newMockResponseWriter returns a response writer
func newMockResponseWriter() *mockResponseWriter {
	header := make(http.Header)
	return &mockResponseWriter{
		staticBuffer: bytes.NewBuffer(nil),
		staticHeader: header,
	}
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

// mockBlocklistResponse is a mock handler for the /skynet/blocklist endpoint
func mockBlocklistResponse(w http.ResponseWriter, r *http.Request) {
	var response BlockResponse
	skyapi.WriteJSON(w, response)
}

// mockResolveResponse is a mock handler for the resolve endpoint, it simply
// resolves the v2 skylink to its v1 counterpart.
func mockResolveResponse(w http.ResponseWriter, r *http.Request) {
	var response resolveResponse
	response.Skylink = v1SkylinkStr
	skyapi.WriteJSON(w, response)
}

// TestHandlers runs the handlers unit tests.
func TestHandlers(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}
	t.Parallel()

	// create a test server that returns mocked responses used by our subtests
	mux := http.NewServeMux()
	mux.HandleFunc("/skynet/blocklist", mockBlocklistResponse)
	mux.HandleFunc(fmt.Sprintf("/skynet/resolve/%s", v2SkylinkStr), mockResolveResponse)
	server := httptest.NewServer(mux)
	defer server.Close()

	tests := []struct {
		name string
		test func(t *testing.T, s *httptest.Server)
	}{
		{
			name: "HandleBlockRequest",
			test: testHandleBlockRequest,
		},
		{
			name: "HandleBlocklistGET",
			test: testHandleBlocklistGET,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) { test.test(t, server) })
	}
}

// testHandleBlockRequest verifies the functionality of the block request
// handler in the API, this method is called by both the regular and PoW block
// routes and contains all shared logic.
func testHandleBlockRequest(t *testing.T, server *httptest.Server) {
	// create a client that connects to our server
	client := NewSkydClient(server.URL, "")

	// create a context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), database.MongoDefaultTimeout)
	defer cancel()

	// create a new test API
	api, err := newTestAPI("HandleBlockRequest", client)
	if err != nil {
		t.Fatal(err)
	}

	// create a response writer
	w := newMockResponseWriter()

	// create skylink
	var sl skymodules.Skylink
	err = sl.LoadString(v1SkylinkStr)
	if err != nil {
		t.Fatal(err)
	}

	// allowlist a skylink
	hash := database.NewHash(sl)
	err = api.staticDB.CreateAllowListedSkylink(ctx, &database.AllowListedSkylink{
		Hash:           hash,
		Description:    "test hash",
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
		t.Fatal("unexpected response status", resp.Status, resp)
	}

	// assert the blocked skylink did not make it into the database
	doc, err := api.staticDB.FindByHash(ctx, database.NewHash(sl))
	if err != nil {
		t.Fatal("unexpected error", err)
	}
	if doc != nil {
		t.Fatal("unexpected blocked skylink found", doc)
	}

	// up until now we have asserted that the skylink gets resolved and the
	// allowlist gets checked, note that this is only meaningful if the below
	// assertions also pass (happy path)

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

// testHandleBlocklistGET verifies the GET /blocklist endpoint
func testHandleBlocklistGET(t *testing.T, server *httptest.Server) {
	// create a client that connects to our server
	client := NewSkydClient(server.URL, "")

	// create a context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), database.MongoDefaultTimeout)
	defer cancel()

	// create a new test API
	api, err := newTestAPI("HandleBlockRequest", client)
	if err != nil {
		t.Fatal(err)
	}
	apiTester := newAPITester(api)

	// fetch the blocklist and assert it is empty
	bl, err := apiTester.blocklistGET(nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bl.HasMore {
		t.Fatal("unexpected")
	}
	if len(bl.Entries) != 0 {
		t.Fatalf("unexpected number of entries, %v != 0", len(bl.Entries))
	}

	// insert 20 documents
	for i := 0; i < 20; i++ {
		tag := fmt.Sprintf("tag_%d", i)
		skylink := fmt.Sprintf("skylink_%d", i)
		offset := time.Duration(i) * time.Second
		err = api.staticDB.CreateBlockedSkylink(ctx, &database.BlockedSkylink{
			Hash: database.HashBytes([]byte(skylink)),
			Reporter: database.Reporter{
				Name: "John Doe",
			},
			Tags:           []string{tag},
			TimestampAdded: time.Now().UTC().Add(offset),
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// assert base case
	bl, err = apiTester.blocklistGET(nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(bl.Entries) != 20 {
		t.Fatalf("unexpected number of entries, %v != 20", len(bl.Entries))
	}
	if bl.HasMore {
		t.Fatal("unexpected", bl)
	}

	// assert default sort
	if len(bl.Entries[0].Tags) != 1 || bl.Entries[0].Tags[0] != "tag_0" ||
		len(bl.Entries[1].Tags) != 1 || bl.Entries[1].Tags[0] != "tag_1" {
		t.Fatal("unexpected sort", bl)
	}

	// assert limit of 1
	limit := 1
	bl, err = apiTester.blocklistGET(nil, nil, &limit)
	if err != nil {
		t.Fatal(err)
	}
	if len(bl.Entries) != 1 {
		t.Fatalf("unexpected number of entries, %v != 1", len(bl.Entries))
	}
	if !bl.HasMore {
		t.Fatal("unexpected", bl)
	}

	// assert offset of 1
	offset := 1
	bl, err = apiTester.blocklistGET(nil, &offset, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(bl.Entries) != 19 {
		t.Fatalf("unexpected number of entries, %v != 19", len(bl.Entries))
	}
	if bl.HasMore {
		t.Fatal("unexpected", bl)
	}
	if len(bl.Entries[0].Tags) != 1 || bl.Entries[0].Tags[0] != "tag_1" {
		t.Fatal("unexpected first entry", bl.Entries[0])
	}

	// assert sort
	sort := "desc"
	bl, err = apiTester.blocklistGET(&sort, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(bl.Entries) != 20 {
		t.Fatalf("unexpected number of entries, %v != 20", len(bl.Entries))
	}
	if bl.HasMore {
		t.Fatal("unexpected", bl)
	}
	// assert 'desc' sort
	if len(bl.Entries[0].Tags) != 1 || bl.Entries[0].Tags[0] != "tag_19" ||
		len(bl.Entries[1].Tags) != 1 || bl.Entries[1].Tags[0] != "tag_18" {
		t.Fatal("unexpected sort", bl)
	}

	// assert pagination
	offset = 0
	limit = 5
	numCalls := 0
	hasmore := true
	var entries []BlockedHash
	for hasmore {
		bl, err = apiTester.blocklistGET(nil, &offset, &limit)
		if err != nil {
			t.Fatal(err)
		}
		numCalls++
		offset += limit
		entries = append(entries, bl.Entries...)
		hasmore = bl.HasMore
	}
	if numCalls != 4 {
		t.Fatalf("unexpected number of calls, %v != 4", numCalls)
	}
	if len(entries) != 20 {
		t.Fatalf("unexpected number of entries, %v != 20", len(entries))
	}
}

// TestParseListParams is a unit test that covers parseListParameters
func TestParseListParams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in  []interface{}
		out []int
		err string
	}{
		// // valid cases
		{[]interface{}{nil, nil, nil}, []int{1, 0, 1000}, ""},
		{[]interface{}{"asc", nil, nil}, []int{1, 0, 1000}, ""},
		{[]interface{}{"desc", nil, nil}, []int{-1, 0, 1000}, ""},
		{[]interface{}{"ASC", nil, nil}, []int{1, 0, 1000}, ""},
		{[]interface{}{"DESC", nil, nil}, []int{-1, 0, 1000}, ""},
		{[]interface{}{nil, 0, nil}, []int{1, 0, 1000}, ""},
		{[]interface{}{nil, 10, nil}, []int{1, 10, 1000}, ""},
		{[]interface{}{nil, nil, 1}, []int{1, 0, 1}, ""},
		{[]interface{}{nil, nil, 10}, []int{1, 0, 10}, ""},

		// invalid cases
		{[]interface{}{"ttt", nil, nil}, []int{0, 0, 0}, "invalid value for 'sort'"},
		{[]interface{}{nil, -1, nil}, []int{0, 0, 0}, "invalid value for 'offset'"},
		{[]interface{}{nil, nil, 0}, []int{0, 0, 0}, "invalid value for 'limit'"},
		{[]interface{}{nil, nil, 1001}, []int{0, 0, 0}, "invalid value for 'limit'"},
	}

	// Test set cases to ensure known edge cases are always handled
	for _, test := range tests {
		params := []string{"sort", "offset", "limit"}

		values := url.Values{}
		for i, key := range params {
			if test.in[i] != nil {
				values.Set(key, fmt.Sprint(test.in[i]))
			}
		}

		sort, offset, limit, err := parseListParameters(values)
		if test.err != "" && err == nil {
			t.Fatalf("Expected error containing '%v' but was nil", test.err)
		}
		if test.err != "" && !strings.Contains(err.Error(), test.err) {
			t.Fatalf("Expected error containing '%v' but was %v", test.err, err.Error())
		}
		if test.err == "" && err != nil {
			t.Fatalf("Expected no error, but received '%v'", err.Error())
		}

		result := []int{sort, offset, limit}
		for i := range params {
			if result[i] != test.out[i] {
				t.Log("Result", result)
				t.Log("Expected", test.out)
				t.Fatal("Unexpected")
			}
		}
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
