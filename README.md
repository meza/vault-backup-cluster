# Vault Snapshot Coordinator

Vault Snapshot Coordinator is a small clustered service that automates periodic snapshots for a HashiCorp Vault cluster running on Integrated Storage. It exists to provide the orchestration and runtime harness that would otherwise need to be done manually when Vault Enterprise automated snapshot features are not available.

The service is designed to run as multiple identical instances. Coordination is handled through Consul so that only one instance performs a backup at a given scheduled time. If the active instance dies or loses leadership, another instance can take over.

This service is intended to run inside an environment where transport trust and short-lived access are already part of the platform model. It relies on Envoy and Vault Agent for connectivity, mTLS, and credential management rather than implementing those concerns itself.

## What it does

The service participates in a Consul-coordinated cluster and competes for backup leadership. The elected leader runs on the configured schedule, calls Vault's snapshot API, writes the snapshot artifact, uploads it to the configured backup destination, records execution metadata, and applies retention policy.

Non-leader instances remain passive and ready to take over if leadership changes.

## Design principles

This project is intentionally narrow.

It is not a general-purpose Vault administration tool.
It is not a replacement for Vault Agent, Envoy, or service mesh identity.
It is not a disaster recovery control plane.

Its job is to coordinate and execute periodic Vault snapshots safely and predictably.

## Runtime model

Each node runs the same container image.
Each instance connects to Consul and competes for leadership using the configured coordination key.
The leader runs the backup workflow on the configured schedule.
Non-leaders wait and monitor leadership state.

The service assumes that Envoy and Vault Agent provide the local trust path and access to Vault and Consul. From the application's point of view, those systems are available through local or otherwise trusted endpoints exposed by the runtime.

## Configuration

Configuration is expected to be provided through container environment variables.

At minimum, the service must support configuration for:

* backup location
* backup schedule
* Vault endpoint
* Consul endpoint
* coordination key
* retention policy
* artifact naming pattern
* scratch storage path
* logging level
* metrics bind address
* any local paths or endpoints exposed by Envoy and Vault Agent

The same image should be reusable across environments without requiring rebuilds for schedule or destination changes.

## Security model

Vault snapshots are sensitive artifacts and must be treated accordingly.

The service must never log snapshot contents, secrets, or credential material. Temporary local artifacts should only exist for as long as needed to complete upload and metadata recording. Uploaded artifacts should be stored outside the Vault cluster footprint using secure transport and encrypted storage.

The service does not own transport identity or credential lifecycle management. Those concerns are delegated to Envoy and Vault Agent.

## Expected behavior

Only the current leader is allowed to execute backups.
A node that loses leadership must stop acting as leader.
A failed backup must never be reported as successful.
A node must not self-promote outside Consul coordination.
The system should remain operational when individual nodes fail.

##
