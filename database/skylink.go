package database

import (
	"fmt"
	"time"

	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.mongodb.org/mongo-driver/bson/bsontype"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/x/bsonx/bsoncore"
	"go.sia.tech/siad/crypto"
)

// Hash is a struct that embeds the crypto.Hash, allowing us to implement the
// bsoncodec ValueMarshaler interfaces.
type Hash struct {
	crypto.Hash
}

// NewHash returns the Hash of the given skylink.
func NewHash(sl skymodules.Skylink) Hash {
	return Hash{crypto.HashObject(sl.MerkleRoot())}
}

// HashBytes returns the Hash of the given bytes.
func HashBytes(b []byte) Hash {
	return Hash{crypto.HashBytes(b)}
}

// MarshalBSONValue implements the bsoncodec.ValueMarshaler interface.
func (h Hash) MarshalBSONValue() (bsontype.Type, []byte, error) {
	return bsontype.String, bsoncore.AppendString(nil, h.String()), nil
}

// UnmarshalBSONValue implements the bsoncodec.ValueUnmarshaler interface.
func (h *Hash) UnmarshalBSONValue(t bsontype.Type, b []byte) error {
	s, _, ok := bsoncore.ReadString(b)
	if !ok {
		return fmt.Errorf("Hash UnmarshalBSONValue error, reading '%s'", string(b))
	}

	var unmarshaled Hash
	err := unmarshaled.LoadString(s)
	if err != nil {
		return err
	}
	*h = unmarshaled
	return nil
}

// DiffHashes is a helper function that returns an array of hashes that are part
// of the base array but are not present in any of the other arrays.
func DiffHashes(array []Hash, others ...[]Hash) []Hash {
	// build a map of hashes to exclude
	seen := make(map[string]struct{})
	for _, other := range others {
		for _, hash := range other {
			seen[hash.String()] = struct{}{}
		}
	}

	var diff []Hash
	for _, hash := range array {
		if _, exists := seen[hash.String()]; !exists {
			diff = append(diff, hash)
		}
	}
	return diff
}

// AllowListedSkylink is a skylink that is allow listed and thus prohibited from
// ever being blocked.
type AllowListedSkylink struct {
	ID             primitive.ObjectID `bson:"_id,omitempty"`
	Skylink        string             `bson:"skylink"`
	Description    string             `bson:"description"`
	TimestampAdded time.Time          `bson:"timestamp_added"`
}

// BlockedSkylink is a skylink blocked by an external request.
type BlockedSkylink struct {
	ID                primitive.ObjectID `bson:"_id,omitempty"`
	Failed            bool               `bson:"failed"`
	Hash              Hash               `bson:"hash"`
	Invalid           bool               `bson:"invalid"`
	Reporter          Reporter           `bson:"reporter"`
	Reverted          bool               `bson:"reverted"`
	RevertedTags      []string           `bson:"reverted_tags"`
	Skylink           string             `bson:"skylink"`
	Tags              []string           `bson:"tags"`
	TimestampAdded    time.Time          `bson:"timestamp_added"`
	TimestampReverted time.Time          `bson:"timestamp_reverted"`
}

// Validate is a small helper function that ensures the required properties are
// set on the BlockedSkylink object.
func (bsl *BlockedSkylink) Validate() error {
	if bsl.Hash == (Hash{}) {
		return errors.New("missing 'Hash' property")
	}
	if bsl.TimestampAdded == (time.Time{}) {
		return errors.New("missing 'TimestampAdded' property")
	}
	return nil
}

// Reporter is a person who reported that a given skylink should be blocked.
type Reporter struct {
	Name            string `bson:"name"`
	Email           string `bson:"email"`
	OtherContact    string `bson:"other_contact"`
	Sub             string `bson:"sub,omitempty"`
	Unauthenticated bool   `bson:"unauthenticated,omitempty"`
}
