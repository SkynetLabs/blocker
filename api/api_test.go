package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	url "net/url"

	"github.com/SkynetLabs/blocker/database"
	"github.com/SkynetLabs/blocker/skyd"
	"github.com/sirupsen/logrus"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// apiTester is a helper struct wrapping handlers of the underlying API that
// record a certain request and parse the API response.
type apiTester struct {
	staticAPI *API
}

// newAPITester returns a new instance of apiTester
func newAPITester(api *API) *apiTester {
	return &apiTester{staticAPI: api}
}

// newTestAPI returns a new API instance
func newTestAPI(dbName string, skyd skyd.API) (*API, error) {
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

	// create a context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), database.MongoDefaultTimeout)
	defer cancel()

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

// blocklistGET records an api call to GET /blocklist on the underlying API
// using the given parameters and returns a parsed response.
func (at *apiTester) blocklistGET(sort *string, offset, limit *int) (BlocklistGET, error) {
	// set url values
	values := url.Values{}
	if offset != nil {
		values.Set("offset", fmt.Sprint(*offset))
	}
	if limit != nil {
		values.Set("limit", fmt.Sprint(*limit))
	}
	if sort != nil {
		values.Set("sort", *sort)
	}

	// execute the request
	var blg BlocklistGET
	err := at.get("/blocklist", values, &blg)
	if err != nil {
		return BlocklistGET{}, err
	}
	return blg, nil
}

// get is a helper function that executes a GET request on the given endpoint
// withe provided query values. It allows passing an object into which we'll
// unmarshal the response body
func (at *apiTester) get(endpoint string, query url.Values, obj interface{}) error {
	// create the request
	url := fmt.Sprintf("%s?%s", endpoint, query.Encode())
	req := httptest.NewRequest(http.MethodGet, url, nil)

	// create a recorder and execute the request
	w := httptest.NewRecorder()
	at.staticAPI.blocklistGET(w, req, nil)
	res := w.Result()
	defer res.Body.Close()

	// handle the response
	data, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return err
	}

	// return an error if the status code is not in the 200s
	if res.StatusCode >= 300 {
		return fmt.Errorf("GET request to '%s' with status %d, response body: %v", endpoint, res.StatusCode, string(data))
	}

	// unmarshal the body into the given object
	err = json.Unmarshal(data, obj)
	if err != nil {
		return err
	}
	return nil
}
