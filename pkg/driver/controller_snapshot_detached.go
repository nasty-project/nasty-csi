package driver

// Detached snapshot support has been removed.
//
// The legacy NASty implementation used ZFS send/receive replication to create
// fully independent dataset copies (detached snapshots). This mechanism does not
// exist in the NASty backend (bcachefs).
//
// TODO: Implement detached snapshots using NASty's native bcachefs clone/copy API
// once such functionality is available.
