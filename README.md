# blocker

Blocker is a service that blocklists all bad skylinks on the current server.

The service exposes a REST API that allows callers to request the blocking of new skylinks.

The blocklist is shared between the servers that make up a portal cluster via MongoDB.

