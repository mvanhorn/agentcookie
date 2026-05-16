# agentcookie

Peer-to-peer Chrome session replication for AI agents.

Your laptop is logged in to everything. Your AI agents run on a different machine (Mac mini, cloud VM, whatever) and aren't. That gap is `agentcookie`.

## Status

Pre-release. v0.1 in development. See the planning doc linked from this commit for the full roadmap.

What works today: a spike (under `cmd/spike-source` and `cmd/spike-sink`) that copies cookies for one hardcoded host from one Mac's Chrome to another Mac's Chrome. Source and sink are separate binaries. Transport is HTTP with an AES-GCM-encrypted payload using a hardcoded shared secret. Chrome must NOT be running on the destination during a write; live-Chrome support via CDP comes in U4 of the plan.

What does not yet exist: pairing handshake, allowlist, fsnotify-driven continuous sync, live-Chrome CDP injection on the sink, install skill, brand, marketing site, threat model doc. All on the roadmap.

## License

Apache 2.0.
