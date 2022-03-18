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
	"github.com/sirupsen/logrus"
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
func newTestAPI(dbName string, client *Client) (*API, error) {
	// create a nil logger
	logger := logrus.New()
	logger.Out = ioutil.Discard

	// create database
	db := database.NewTestDB(context.Background(), dbName, logger)

	// create the API
	api, err := New(client, db, logger)
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
// with the provided query values. The response will get unmarshaled into the
// given response object.
func (at *apiTester) get(endpoint string, query url.Values, obj interface{}) error {
	// create the request
	url := fmt.Sprintf("%s?%s", endpoint, query.Encode())
	req := httptest.NewRequest(http.MethodGet, url, nil)

	// create a recorder and execute the request
	w := httptest.NewRecorder()
	at.staticAPI.blocklistGET(w, req, nil)
	res := w.Result()
	defer drainAndClose(res.Body)

	// return an error if the status code is not in the 200s
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("GET request to '%s' with status %d error %v", endpoint, res.StatusCode, readAPIError(res.Body))
	}

	// handle the response body
	err := json.NewDecoder(res.Body).Decode(obj)
	if err != nil {
		return err
	}
	return nil
}
