// Package storagecodec shares the bounded structured-clone codec between the
// Gecko companion and Chrome's synthetic, network-isolated helper target.
package storagecodec

import _ "embed"

// CodecJS installs AgentCookieCodec in the MAIN world.
//
//go:embed codec.js
var CodecJS string

// ReaderJS installs AgentCookieStorageReader in the MAIN world.
//
//go:embed reader.js
var ReaderJS string

// ApplierJS installs AgentCookieStorageApplier in the MAIN world.
//
//go:embed applier.js
var ApplierJS string

// CompanionJS installs the privileged readStorage handler, including cleanup.
//
//go:embed read-storage.js
var CompanionJS string

// CompanionAssets are copied into the private temporary extension directory.
func CompanionAssets() map[string]string {
	return map[string]string{
		"codec.js": CodecJS, "reader.js": ReaderJS,
		"read-storage.js": CompanionJS,
	}
}
