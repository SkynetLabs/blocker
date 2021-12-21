package blocker

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
)

var (
	// maxTarget is the target with the highest difficulty possible.
	maxTarget = [proofHashSize]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}

	// minTarget is the target with the lowest difficulty possible.
	minTarget = [proofHashSize]byte{255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255}
)

// Solve solves the pow for a given proof.
func (proof *BlockPoW) Solve(target []byte) {
	for i := uint64(0); i < math.MaxUint64; i++ {
		// Update the nonce.
		binary.LittleEndian.PutUint64(proof.Nonce[:], i)

		// Hash the proof.
		work := hashMySkyProof(proof.ProofBytes())

		// Compare it to the target.
		if bytes.Compare(target, work[:]) > 0 {
			break // done
		}
	}
}

// TestMySkyProof runs all tests related to MySkyProofs.
func TestMySkyProof(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		t    func(t *testing.T)
	}{
		{
			name: "Nonce",
			t:    testMySkyNonce,
		},
		{
			name: "Version",
			t:    testMySkyProofVersion,
		},
		{
			name: "ID",
			t:    testMySkyID,
		},
		{
			name: "ProofBytes",
			t:    testMySkyProofBytes,
		},
		{
			name: "Verify",
			t:    testMySkyProofVerify,
		},
	} {
		t.Run(test.name, test.t)
	}
}

// testMySkyNonce tests marshaling/unmarshaling a nonce to/from json.
func testMySkyNonce(t *testing.T) {
	var nonce mySkyProofNonce
	binary.LittleEndian.PutUint64(nonce[:], 12345678)

	// Marshal
	b, err := json.Marshal(nonce)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "\"12345678\"" {
		t.Fatal("invalid result", string(b))
	}

	// Unmarshal
	var nonce2 mySkyProofNonce
	err = json.Unmarshal(b, &nonce2)
	if err != nil {
		t.Fatal(err)
	}

	// Compare
	if nonce != nonce2 {
		t.Fatal("wrong reuslt", nonce, nonce2)
	}

	// Unmarshal invalid.
	invalidNonce, err := json.Marshal("-1")
	if err != nil {
		t.Fatal(err)
	}
	err = json.Unmarshal([]byte(invalidNonce), &nonce2)
	if err == nil || !strings.Contains(err.Error(), "expected integer") {
		t.Fatal("should fail", err)
	}
}

// testMySkyNonce tests marshaling/unmarshaling a proof version to/from json.
func testMySkyProofVersion(t *testing.T) {
	invalidVersion := mySkyProofVersion(0)
	validVersion := proofVersionV1Byte

	// Marshal invalid.
	_, err := json.Marshal(invalidVersion)
	if !strings.Contains(err.Error(), errInvalidVersion.Error()) { // use strings.Contains since json.Unmarshal prevents errors.Contains from being used here.
		t.Fatal("should fail", err)
	}

	// Marshal
	b, err := json.Marshal(validVersion)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != fmt.Sprintf("\"%s\"", proofVersionV1) {
		t.Fatal("wrong result", string(b))
	}

	// Unmarshal
	var validVersion2 mySkyProofVersion
	err = json.Unmarshal(b, &validVersion2)
	if err != nil {
		t.Fatal(err)
	}

	// Compare
	if validVersion != validVersion2 {
		t.Fatal("wrong result", validVersion, validVersion2)
	}

	// Unmarshal invalid.
	invalidVersionStr := "\"invalid\""
	err = json.Unmarshal([]byte(invalidVersionStr), &validVersion2)
	if !errors.Contains(err, errInvalidVersion) {
		t.Fatal("should fail", err)
	}
}

// testMySkyID tests marshaling/unmarshaling a MySkyID to/from json.
func testMySkyID(t *testing.T) {
	pk, _, err := ed25519.GenerateKey(fastrand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var msid mySkyID
	copy(msid[:], pk)

	// Marshal
	b, err := json.Marshal(msid)
	if err != nil {
		t.Fatal(err)
	}

	msidStr := hex.EncodeToString(pk)
	if string(b) != fmt.Sprintf("\"%s\"", msidStr) {
		t.Fatal("wrong result", string(b), msidStr)
	}

	// Unmarshal
	var msid2 mySkyID
	err = json.Unmarshal(b, &msid2)
	if err != nil {
		t.Fatal(err)
	}

	// Compare
	if msid != msid2 {
		t.Fatal("wrong result", msid, msid2)
	}

	// Unmarshal invalid.
	invalidID := "\"010203\""
	err = json.Unmarshal([]byte(invalidID), &msid2)
	if !errors.Contains(err, errInvalidIDLength) {
		t.Fatal("should fail", err)
	}
}

// testMySkyProofBytes is a unit-test for the ProofBytes method.
func testMySkyProofBytes(t *testing.T) {
	// Init a proof in a way that the proof bytes end up being the bytes from 1 to 40.
	proof := BlockPoW{
		Version: proofVersionV1Byte,
		Nonce:   mySkyProofNonce{2, 3, 4, 5, 6, 7, 8, 9},
	}
	for i := range proof.MySkyID {
		proof.MySkyID[i] = byte(i + 10)
	}

	// Check length.
	proofBytes := proof.ProofBytes()
	if len(proofBytes) != 41 {
		t.Fatal("invalid length", len(proofBytes))
	}
	for i := range proofBytes {
		if proofBytes[i] != byte(i+1) {
			t.Fatalf("wrong value %v at offset %v, expect %v", proofBytes[i], i, i+1)
		}
	}
}

// testMySkyProofVerify is a unit test for the proof's Verify method.
func testMySkyProofVerify(t *testing.T) {
	// Create valid msid.
	pk, sk, err := ed25519.GenerateKey(fastrand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var msid mySkyID
	copy(msid[:], pk)

	// Create proof.
	validProof := BlockPoW{
		Version: proofVersionV1Byte,
		Nonce:   mySkyProofNonce{1, 2, 3, 4, 5, 6, 7, 8},
		MySkyID: msid,
	}

	// Sign it and add the signature to the proof.
	msg := validProof.SignMessage()
	validProof.Signature = ed25519.Sign(sk, msg[:])

	// Verify the proof against the smallest target possible. Regardless of
	// nonce this should always work.
	if err := validProof.verify(minTarget); err != nil {
		t.Fatal(err)
	}

	// Compare against the largest target. This should never work.
	if err := validProof.verify(maxTarget); !errors.Contains(err, errInsufficientWork) {
		t.Fatal(err)
	}

	// Compare against the min target but corrupt the signature.
	invalidProof := validProof
	invalidProof.Signature = fastrand.Bytes(len(invalidProof.Signature))
	if err := invalidProof.verify(minTarget); !errors.Contains(err, errInvalidSignature) {
		t.Fatal(err)
	}
}

// TestFindTarget is a test that can be run to identify a good target on a given
// CPU for a given target duration.
// NOTE: Commented out since it's only meant to be run manually and to avoid
// pulling in Sia as a dependency.
//func TestFindTarget(t *testing.T) {
//	proof := BlockPoW{
//		Version: proofVersionV1Byte,
//	}
//
//	target := types.Target{0, 0, 2, 85, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255}
//	targetDuration := 50 * time.Second
//
//	targetHits := 0
//	finishHits := 10
//	maxDiffIncrease := big.NewRat(10005, 10000) // 0.05%
//	maxDiffDecrease := big.NewRat(9998, 10000)  // 0.02%
//	for {
//		start := time.Now()
//		proof.Solve(target[:])
//		d := time.Since(start)
//		fmt.Println("duration", d, target)
//
//		if d > targetDuration {
//			fmt.Println("Hit target:", targetHits)
//			targetHits++
//		}
//		if targetHits == finishHits {
//			fmt.Println("Done!!!")
//			return
//		}
//
//		delta := big.NewRat(int64(targetDuration), int64(d))
//		proposedDelta := delta
//		if delta.Cmp(maxDiffIncrease) > 0 {
//			delta = maxDiffIncrease
//		} else if delta.Cmp(maxDiffDecrease) < 0 {
//			delta = maxDiffDecrease
//		}
//
//		fmt.Println("change", delta.FloatString(8), proposedDelta)
//
//		target = target.MulDifficulty(delta)
//		fmt.Println("new target", target)
//	}
//}
