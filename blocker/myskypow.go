package blocker

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/SkynetLabs/skynet-accounts/build"
	"github.com/mimoo/GoKangarooTwelve/K12"
	"gitlab.com/NebulousLabs/errors"
	"golang.org/x/crypto/ed25519"
	"golang.org/x/crypto/sha3"
)

// mySkyTarget is the target a proof needs to meet to be considered valid.
// The Standard target was chosen empirically by running the algorithm on a i9
// until the time it takes to solve the pow averaged out around 60s.
var mySkyTarget = build.Select(build.Var{
	Dev:      [proofHashSize]byte{0, 0, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255},
	Testing:  [proofHashSize]byte{0, 0, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255, 255},
	Standard: [proofHashSize]byte{0, 0, 2, 79, 134, 217, 6, 168, 28, 68, 106, 164, 207, 53, 55, 178, 24, 81, 162, 117, 144, 30, 90, 200, 147, 120, 124, 181, 32, 216, 184, 223},
}).([proofHashSize]byte)

const (
	// proofVersionV1 is the string representation of the first version of
	// the proof used in the API.
	proofVersionV1 = "MySkyID-PoW-v1"

	// proofVersionV1Byte is the byte representation of the first version of
	// the proof used for hashing and signing.
	proofVersionV1Byte = mySkyProofVersion(1)

	// proofHashSize defines the size of the hash used for the pow
	// algorithm.
	proofHashSize = 32
)

var (
	// errInvalidLength is returned if the MySkyID has an unexpected length.
	errInvalidIDLength = errors.New("invalid MySkyID length")

	// errInvalidVersion is returned if the proof has an unexpected version.
	errInvalidVersion = errors.New("invalid version")

	// errInsufficientWork is returned if the hash of the byte
	// representation of the proof doesn't meet the difficulty target.
	errInsufficientWork = errors.New("insufficient work")

	// errInvalidSignature is returned if the signature of a proof doesn't
	// match its byte representation.
	errInvalidSignature = errors.New("invalid signature")

	// proofHashIdentifier is the salt for the K12 hashing algorithm.
	proofHashIdentifier = []byte("MySkyProof")

	// myskySignSalt is the salt for the hash of the proof which is then
	// signed.
	myskySignSalt = []byte("MYSKY_ID_VERIFICATION")
)

type (
	// hexBytes is a helper type to marshal/unmarshal a byte slice to/from a
	// hex-encoded string.
	hexBytes []byte

	// mySkyProofNonce is a helper type to marshal/unmarshal a nonce to/from
	// a little endian encoded byte array.
	mySkyProofNonce [8]byte

	// mySkyProofVersion is a helper type to marshal/unmarshal a proof
	// version to/from its string representation.
	mySkyProofVersion byte

	// mySkyID is a helper type to marshal/unmarshal a MySkyID to/from its
	// string representation.
	mySkyID [ed25519.PublicKeySize]byte
)

// BlockPoW describes a proof used to verify some time has passed since
// creating a MySkyID. The fields use custom types which implement the
// json.Marshaler and json.Unmarshaler interfaces. That way it can be read from
// an http request's body.
//
// Example proof:
//
// {
//   "version": "MySkyID-PoW-v1",
//   "nonce": 578437695752307201,
//   "myskyid": "c95988a42db14ab3f8742980becfa2018132116d64b085004273de888ea6e44b",
//   "signature": "cf45f2cf6ce78ae90fdd56e0b3845b977f2926107d5afb366f11e4882955f0f4d5065c7536fb1932fc00c7111c3dfd1a786d06e50b91fe828f05d0587ade990f"
// }
type BlockPoW struct {
	Version   mySkyProofVersion `json:"version"`
	Nonce     mySkyProofNonce   `json:"nonce"`
	MySkyID   mySkyID           `json:"myskyid"`
	Signature hexBytes          `json:"signature"`
}

// MarshalJSON implements the json.Marshaler interface.
func (n mySkyProofNonce) MarshalJSON() ([]byte, error) {
	// turn number into string
	str := fmt.Sprint(binary.LittleEndian.Uint64(n[:]))
	return json.Marshal(str)
}

// UnmarshalJSON implements the json.Unmarshaler interface.
func (n *mySkyProofNonce) UnmarshalJSON(b []byte) error {
	var nonceStr string
	err := json.Unmarshal(b, &nonceStr)
	if err != nil {
		return err
	}
	var nonce uint64
	_, err = fmt.Sscan(nonceStr, &nonce)
	if err != nil {
		return err
	}
	binary.LittleEndian.PutUint64(n[:], nonce)
	return nil
}

// MarshalJSON implements the json.Marshaler interface.
func (hb hexBytes) MarshalJSON() ([]byte, error) {
	bytes := hex.EncodeToString(hb)
	return json.Marshal(bytes)
}

// UnmarshalJSON implements the json.Unmarshaler interface.
func (hb *hexBytes) UnmarshalJSON(b []byte) error {
	var bytesStr string
	err := json.Unmarshal(b, &bytesStr)
	if err != nil {
		return err
	}
	*hb, err = hex.DecodeString(bytesStr)
	if err != nil {
		return err
	}
	return nil
}

// MarshalJSON implements the json.Marshaler interface.
func (v mySkyProofVersion) MarshalJSON() ([]byte, error) {
	var versionStr string
	switch v {
	case 1:
		versionStr = proofVersionV1
	default:
		return nil, errors.AddContext(errInvalidVersion, fmt.Sprint(v))
	}
	return json.Marshal(versionStr)
}

// UnmarshalJSON implements the json.Unmarshaler interface.
func (v *mySkyProofVersion) UnmarshalJSON(b []byte) error {
	var versionStr string
	err := json.Unmarshal(b, &versionStr)
	if err != nil {
		return err
	}
	var version mySkyProofVersion
	switch versionStr {
	case proofVersionV1:
		version = proofVersionV1Byte
	default:
		return errors.AddContext(errInvalidVersion, fmt.Sprint(v))
	}
	*v = version
	return nil
}

// MarshalJSON implements the json.Marshaler interface.
func (msid mySkyID) MarshalJSON() ([]byte, error) {
	id := hex.EncodeToString(msid[:])
	return json.Marshal(id)
}

// UnmarshalJSON implements the json.Unmarshaler interface.
func (msid *mySkyID) UnmarshalJSON(b []byte) error {
	var id hexBytes
	err := json.Unmarshal(b, &id)
	if err != nil {
		return err
	}
	if len(id) != len(msid) {
		return errors.AddContext(errInvalidIDLength, fmt.Sprintf("%v != %v", len(id), len(msid)))
	}
	copy(msid[:], id)
	return nil
}

// ProofBytes returns a byte presentation of the MySkyProof which can be hashed
// to compare to a target and hashed+signed for a signature.
func (p *BlockPoW) ProofBytes() []byte {
	b := make([]byte, 1+len(p.Nonce)+ed25519.PublicKeySize)

	// Set version
	offset := 0
	b[0] = byte(p.Version)
	offset++

	// Set nonce
	copy(b[offset:offset+len(p.Nonce)], p.Nonce[:])
	offset += len(p.Nonce)

	// PublicKey
	copy(b[offset:offset+len(p.MySkyID)], p.MySkyID[:])

	return b
}

// PublicKey is a helper to get the ed25519.PublicKey from the MySkyID.
func (p *BlockPoW) PublicKey() ed25519.PublicKey {
	return ed25519.PublicKey(p.MySkyID[:])
}

// Verify verifies the proof against the mySkyTarget.
func (p BlockPoW) Verify() error {
	return p.verify(mySkyTarget)
}

// verify verifies the proof. This includes verifying the signature and then
// verifying if the work used to create the proof is sufficient to meet the
// given target.
func (p BlockPoW) verify(target [proofHashSize]byte) error {
	// Get the proof bytes.
	b := p.ProofBytes()

	// Salt them.
	msg := sha3.Sum512(append(myskySignSalt, b...))

	// Verify Signature.
	pk := p.PublicKey()
	if !ed25519.Verify(pk, msg[:], p.Signature) {
		return errInvalidSignature
	}

	// Verify PoW.
	work := hashMySkyProof(b)
	if bytes.Compare(target[:], work[:]) <= 0 {
		return errInsufficientWork
	}
	return nil
}

// hashMySkyProof is a helper to hash a proof which allows us to swap the
// hashing algorithm by only updating one function instead of all the places
// where we call it.
func hashMySkyProof(proof []byte) (hash [proofHashSize]byte) {
	K12.K12Sum(proofHashIdentifier, proof, hash[:])
	return
}
