# blocker

Blocker is a service that blocklists all bad skylinks on the current server.

The service exposes a REST API that allows callers to request the blocking of new skylinks.

The blocklist is shared between the servers that make up a portal cluster via MongoDB.

# AllowList

The blocker service can only block skylinks which are not in the allow list.
To add a skylink to the allow list, one has to manually query the database and
perform the follow operation:

```
db.getCollection('allowlist').insertOne({
  skylink: "[INSERT V1 SKYLINK HERE]",
  description: "[INSERT SKYLINK DESCRIPTION]",
  timestamp_added: new Date(),
})
```

The skylink is expected to be in the following form: `_B19BtlWtjjR7AD0DDzxYanvIhZ7cxXrva5tNNxDht1kaA`.
So that's without portal and without the `sia://` prefix.

# Environment

This service depends on the following environment variables:
* `API_HOST`, e.g. `sia` (defaults to `sia`)
* `API_PORT`, e.g. `9980` (defaults to `9980`)
* `SIA_API_PASSWORD`
* `SKYNET_DB_HOST`
* `SKYNET_DB_PORT`
* `SKYNET_DB_USER`
* `SKYNET_DB_PASS`
* `SKYNET_ACCOUNTS_HOST`, defaults to `accounts`
* `SKYNET_ACCOUNTS_PORT`, defaults to `3000`
* `SERVER_DOMAIN`, e.g. `eu-ger-5.siasky.net`
* `PORTAL_DOMAIN`, e.g. `siasky.net`
* `BLOCKER_LOG_LEVEL`, defaults to `info`

## Blocker Identifier

For the blocker module to work properly, it must have a unique identifier. In a
multi-server setup, this should be configured through the `SERVER_DOMAIN`
environemnt variable. In a single-server setup however, the `SERVER_DOMAIN`
environment variable is not set, in that case the blocker will fall back to
using the `PORTAL_DOMAIN` as a unique identifier for the blocker module.
