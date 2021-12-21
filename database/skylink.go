package database

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

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
