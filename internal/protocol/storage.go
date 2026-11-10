package protocol

import "encoding/json"

// Version3 is the current negotiated snapshot contract. Version remains 2
// for legacy emitters and the ppcli lane; live destinations negotiate Version3.
const Version3 = 3
const StorageIRVersion = 1
const (
	MaxValueDepth             = 64
	MaxValueNodes             = 100000
	MaxRecordBytes            = 16 << 20
	MaxLocalStorageValueBytes = 10 << 20
	MaxDatabaseBytes          = 64 << 20
	MaxSurfaceBytes           = 128 << 20
)

type Surface string

const (
	Cookies      Surface = "cookies"
	LocalStorage Surface = "local_storage"
	IndexedDB    Surface = "indexed_db"
)

// Complete authorizes removal of owned absent records; incomplete retains
// previous records, while unsupported never touches the surface.
const (
	Complete    = "complete"
	Incomplete  = "incomplete"
	Unsupported = "unsupported"
	Shipped     = "shipped"
	Held        = "held"
	Excluded    = "excluded"
)

type SurfaceStatus struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}
type SiteStatus struct {
	State      string   `json:"state"`
	Container  uint32   `json:"container,omitempty"`
	Containers []uint32 `json:"containers,omitempty"`
	Reason     string   `json:"reason,omitempty"`
}
type InventoryStatus struct {
	Listed     bool `json:"listed"`
	Unread     int  `json:"unread,omitempty"`
	Unreadable int  `json:"unreadable,omitempty"`
}
type SnapshotMeta struct {
	Generation         uint64                    `json:"generation"`
	Surfaces           map[Surface]SurfaceStatus `json:"surfaces"`
	Sites              map[string]SiteStatus     `json:"sites,omitempty"`
	Inventory          InventoryStatus           `json:"inventory"`
	AuthoritativeEmpty bool                      `json:"authoritative_empty,omitempty"`
}

// StorageRecords is the browser-independent structured-clone IR v1. Values
// remain raw tagged JSON so float, UTF-16, references and insertion order are
// not corrupted by decoding into interface{}. Consumers must validate the
// bounded graph grammar before allocation or browser execution.
type StorageRecords struct {
	Version int             `json:"version"`
	Origins []StorageOrigin `json:"origins"`
}
type StorageOrigin struct {
	Origin             string             `json:"origin"`
	Container          uint32             `json:"container"`
	LocalStorage       []LocalStorageItem `json:"local_storage,omitempty"`
	IndexedDB          []IDBDatabase      `json:"indexed_db,omitempty"`
	LocalStorageStatus SurfaceStatus      `json:"local_storage_status"`
	IndexedDBStatus    SurfaceStatus      `json:"indexed_db_status"`
}
type LocalStorageItem struct {
	Key   json.RawMessage `json:"key"`
	Value json.RawMessage `json:"value"`
}

// Database, store and index names use tagged Str too: DOMString permits
// lone UTF-16 surrogates, which ordinary Go strings would replace on decode.
type IDBDatabase struct {
	Name    json.RawMessage `json:"name"`
	Version uint64          `json:"version"`
	Stores  []IDBStore      `json:"stores"`
	Status  SurfaceStatus   `json:"status"`
}
type IDBStore struct {
	Name          json.RawMessage `json:"name"`
	KeyPath       json.RawMessage `json:"keyPath"`
	AutoIncrement bool            `json:"autoIncrement"`
	Generator     *float64        `json:"generator,omitempty"`
	Indexes       []IDBIndex      `json:"indexes"`
	Records       []IDBRecord     `json:"records"`
}
type IDBIndex struct {
	Name       json.RawMessage `json:"name"`
	KeyPath    json.RawMessage `json:"keyPath"`
	Unique     bool            `json:"unique"`
	MultiEntry bool            `json:"multiEntry"`
}
type IDBRecord struct {
	Key   json.RawMessage `json:"key"`
	Value json.RawMessage `json:"value"`
}
