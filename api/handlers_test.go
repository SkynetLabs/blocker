package api

import (
	"encoding/json"
	"testing"
)

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
