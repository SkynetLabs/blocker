package database

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

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
	Skylink           string             `bson:"skylink"`
	Reporter          Reporter           `bson:"reporter"`
	Reverted          bool               `bson:"reverted"`
	RevertedTags      []string           `bson:"reverted_tags"`
	Tags              []string           `bson:"tags"`
	TimestampAdded    time.Time          `bson:"timestamp_added"`
	TimestampReverted time.Time          `bson:"timestamp_reverted"`
}

// Reporter is a person who reported that a given skylink should be blocked.
type Reporter struct {
	Name            string `bson:"name"`
	Email           string `bson:"email"`
	OtherContact    string `bson:"other_contact"`
	Sub             string `bson:"sub,omitempty"`
	Unauthenticated bool   `bson:"unauthenticated,omitempty"`
}
