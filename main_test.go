package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"gitlab.com/NebulousLabs/errors"
)

// TestSanitizePortalURL is a unit test for the sanitizePortalURL helper
func TestSanitizePortalURL(t *testing.T) {
	cases := []struct {
		input  string
		output string
	}{
		{"https://siasky.net", "https://siasky.net"},
		{"https://siasky.net ", "https://siasky.net"},
		{" https://siasky.net ", "https://siasky.net"},
		{"https://siasky.net/", "https://siasky.net"},
		{"http://siasky.net", "https://siasky.net"},
		{"siasky.net", "https://siasky.net"},
	}

	// Test set cases to ensure known edge cases are always handled
	for _, test := range cases {
		res := sanitizePortalURL(test.input)
		if res != test.output {
			t.Fatalf("unexpected result, %v != %v", res, test.output)
		}
	}
}

// TestLoadPortalURLs is a unit test that covers the functionality of the
// 'loadPortalURLs' helper.
func TestLoadPortalURLs(t *testing.T) {
	t.Parallel()

	// create a function to restore the environment
	restoreEnvFn := restoreEnv([]string{"BLOCKER_PORTALS_SYNC"})
	defer func() {
		err := restoreEnvFn()
		if err != nil {
			t.Error(err)
		}
	}()

	// empty case
	os.Setenv("BLOCKER_PORTALS_SYNC", "")
	urls := loadPortalURLs()
	if len(urls) != 0 {
		t.Fatal("unexpected", urls)
	}

	// assert url is sanitized
	os.Setenv("BLOCKER_PORTALS_SYNC", "siasky.net/")
	urls = loadPortalURLs()
	if len(urls) != 1 && urls[0] != "https://siasky.net" {
		t.Fatal("unexpected", urls)
	}

	// assert it can handle multiple items and bad formatting
	os.Setenv("BLOCKER_PORTALS_SYNC", "siasky.net/, skyportal.xyz,,")
	urls = loadPortalURLs()
	if len(urls) != 2 {
		t.Fatal("unexpected", urls)
	}
	sort.Strings(urls)
	if urls[0] != "https://siasky.net" || urls[1] != "https://skyportal.xyz" {
		t.Fatal("unexpected", urls)
	}
}

// TestLoadDBCredentials is a unit test that covers the functionality of the
// 'loadDBCredentials' helper.
func TestLoadDBCredentials(t *testing.T) {
	t.Parallel()

	variables := []string{
		"SKYNET_DB_USER",
		"SKYNET_DB_PASS",
		"SKYNET_DB_HOST",
		"SKYNET_DB_PORT",
	}

	// create a function to restore the environment
	restoreEnvFn := restoreEnv(variables)
	defer func() {
		err := restoreEnvFn()
		if err != nil {
			t.Error(err)
		}
	}()

	// set every env variable to its name
	for _, variable := range variables {
		os.Setenv(variable, variable)
	}

	// load db credentials and assert its output (happy case)
	connstring, credentials, err := loadDBCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if credentials.Username != "SKYNET_DB_USER" || credentials.Password != "SKYNET_DB_PASS" {
		t.Fatal("unexpected", credentials)
	}
	if connstring != "mongodb://SKYNET_DB_HOST:SKYNET_DB_PORT" {
		t.Fatal("unexpected", connstring)
	}

	// unset every env variable one by one and assert the helper indicates what
	// environment variable is missing
	for _, variable := range variables {
		bkp := os.Getenv(variable)
		err = os.Unsetenv(variable)
		if err != nil {
			t.Fatal(err)
		}

		_, _, err := loadDBCredentials()
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("missing env var %v", variable)) {
			t.Fatal("unexpected outcome", err)
		}

		// put it back
		err = os.Setenv(variable, bkp)
		if err != nil {
			t.Fatal(err)
		}
	}
}

// TestRestoreEnv is small unit test that covers the restoreEnv helper
func TestRestoreEnv(t *testing.T) {
	t.Parallel()

	// assert it can handle nil
	restoreFn := restoreEnv(nil)
	err := restoreFn()
	if err != nil {
		t.Fatal(err)
	}

	// set an env variable to some value
	varName := "TestRestoreEnv"
	err = os.Setenv(varName, "somevalue")
	if err != nil {
		t.Fatal(err)
	}

	// create the function
	restoreFn = restoreEnv([]string{varName})

	// update the env variable and assert it's set
	os.Setenv(varName, "somenewvalue")
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv(varName) != "somenewvalue" {
		t.Fatal("unexpected", os.Getenv(varName))
	}

	// restore the env and assert it got restored
	err = restoreFn()
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv(varName) != "somevalue" {
		t.Fatal("unexpected", os.Getenv(varName))
	}
}

// restoreEnv is a helper function that returns a function that, when executed,
// restores the environment to the point restoreEnv got called. It restores the
// environment only for the given set of environment variable names.
func restoreEnv(variables []string) func() error {
	backup := make(map[string]string)
	for _, variable := range variables {
		backup[variable] = os.Getenv(variable)
	}
	return func() error {
		var errs []error
		for key, value := range backup {
			if err := os.Setenv(key, value); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Compose(errs...)
	}
}
