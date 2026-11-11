package storagecodec

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/mvanhorn/agentcookie/internal/protocol"
)

type CompanionCaller interface {
	Call(context.Context, string, any, any) error
}
type ReadBroker interface {
	AddReadIntercept(context.Context, string, string) error
	RemoveReadIntercept(context.Context, string) error
}
type OriginRequest struct {
	Origin        string
	Container     uint32
	Due           bool
	Representable bool
	Reason        string
}
type ReadOutcome struct {
	Origin    string
	Container uint32
	Empty     bool
	Err       error
}
type CycleResult struct {
	Records  *protocol.StorageRecords
	Surfaces map[protocol.Surface]protocol.SurfaceStatus
	Outcomes []ReadOutcome
}
type cachedOrigin struct {
	Data        protocol.StorageOrigin `json:"data"`
	AttemptedAt time.Time              `json:"attempted_at"`
	ReadAt      time.Time              `json:"read_at"`
}

// Source preserves the last readable data independently per surface/database.
// A missing cache forces fresh reads even if the catalog survived a daemon crash.
// All state belongs in the caller's private per-profile state directory.
type ReadSurfaces struct {
	LocalStorage bool `json:"local_storage"`
	IndexedDB    bool `json:"indexed_db"`
}

func (r *ReadSurfaces) Enabled() bool { return r == nil || r.LocalStorage || r.IndexedDB }
func completeOrDisabled(s protocol.SurfaceStatus) bool {
	return s.State == protocol.Complete || s.State == protocol.Unsupported
}

type Source struct {
	Surfaces                            *ReadSurfaces
	mu                                  sync.Mutex
	Mode, CachePath                     string
	CycleBudget, OriginBudget           time.Duration
	cache                               map[string]cachedOrigin
	surfaceLimit, wireLimit, cacheLimit int
}

func NewSource(mode, cachePath string) (*Source, error) {
	if mode == "" {
		mode = "intercept"
	}
	if mode != "intercept" && mode != "request" {
		return nil, errors.New("invalid storage read mode")
	}
	s := &Source{Mode: mode, CachePath: cachePath, CycleBudget: 30 * time.Second, OriginBudget: 3 * time.Second, cache: map[string]cachedOrigin{}, surfaceLimit: protocol.MaxSurfaceBytes, wireLimit: 240 << 20, cacheLimit: 256 << 20}
	if cachePath != "" {
		f, err := os.Open(cachePath)
		if err == nil {
			defer f.Close()
			info, err := f.Stat()
			if err != nil {
				return nil, err
			}
			if info.Size() > int64(s.cacheLimit) {
				return nil, errors.New("storage cache bytes limit")
			}
			b, err := io.ReadAll(io.LimitReader(f, int64(s.cacheLimit)+1))
			if err != nil {
				return nil, err
			}
			if len(b) > s.cacheLimit {
				return nil, errors.New("storage cache bytes limit")
			}
			if err = json.Unmarshal(b, &s.cache); err != nil {
				return nil, err
			}
			if s.cache == nil {
				s.cache = map[string]cachedOrigin{}
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}

	return s, nil
}
func originKey(o string, c uint32) string { return fmt.Sprintf("%s|%d", o, c) }
func incomplete(reason string) protocol.SurfaceStatus {
	return protocol.SurfaceStatus{State: protocol.Incomplete, Reason: reason}
}
func (s *Source) save() error {
	if s.CachePath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.CachePath), 0700); err != nil {
		return err
	}
	if _, err := JSONSize(s.cache, s.cacheLimit); err != nil {
		return errors.New("storage cache bytes limit")
	}
	b, err := json.Marshal(s.cache)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.CachePath), ".storage-cache-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), s.CachePath); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(s.CachePath))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func (s *Source) ReadOrigin(ctx context.Context, companion CompanionCaller, broker ReadBroker, request OriginRequest) (protocol.StorageOrigin, error) {
	var out protocol.StorageOrigin
	if err := ValidOrigin(request.Origin); err != nil {
		return out, err
	}
	if companion == nil {
		return out, errors.New("companion disconnected")
	}
	if s.Mode == "intercept" && broker == nil {
		return out, errors.New("storage reads paused (BiDi slot held by a previous broker session); set storage_read_mode: request or restart the browser")
	}
	var random [24]byte
	if _, err := rand.Read(random[:]); err != nil {
		return out, err
	}
	nonce := hex.EncodeToString(random[:])
	budget := s.OriginBudget
	if budget <= 0 || budget > 3*time.Second {
		budget = 3 * time.Second
	}
	// The page/tab has its own three-second deadline; chunk transfer shares
	// the caller's remaining cycle budget, never a fresh thirty seconds per origin.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if s.Mode == "intercept" {
		if err := broker.AddReadIntercept(ctx, request.Origin, nonce); err != nil {
			return out, err
		}
		defer func() {
			cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			defer stop()
			_ = broker.RemoveReadIntercept(cleanup, nonce)
		}()
	}
	store := "firefox-default"
	if request.Container != 0 {
		store = fmt.Sprintf("firefox-container-%d", request.Container)
	}
	args := map[string]any{"origin": request.Origin, "container": request.Container, "cookieStoreId": store, "nonce": nonce, "mode": s.Mode, "chunked": true, "budgetMS": budget.Milliseconds()}
	if s.Surfaces != nil {
		args["localStorage"] = s.Surfaces.LocalStorage
		args["indexedDB"] = s.Surfaces.IndexedDB
	}
	if err := companion.Call(ctx, "readStorage", args, &out); err != nil {
		return out, err
	}
	if out.Origin != request.Origin || out.Container != request.Container {
		return protocol.StorageOrigin{}, errors.New("storage result identity mismatch")
	}
	return out, nil
}
func mergeOrigin(old, next protocol.StorageOrigin) protocol.StorageOrigin {
	if next.LocalStorageStatus.State != protocol.Complete {
		next.LocalStorage = old.LocalStorage
	}
	previous := map[string]protocol.IDBDatabase{}
	for _, db := range old.IndexedDB {
		previous[string(db.Name)] = db
	}
	seen := map[string]bool{}
	for n, db := range next.IndexedDB {
		k := string(db.Name)
		seen[k] = true
		if db.Status.State != protocol.Complete {
			if prior, ok := previous[k]; ok {
				prior.Status = db.Status
				next.IndexedDB[n] = prior
			}
		}
	}
	if next.IndexedDBStatus.State != protocol.Complete {
		for k, db := range previous {
			if !seen[k] {
				db.Status = incomplete(next.IndexedDBStatus.Reason)
				next.IndexedDB = append(next.IndexedDB, db)
			}
		}
	}
	return next
}
func (s *Source) ReadCycle(ctx context.Context, companion CompanionCaller, broker ReadBroker, requests []OriginRequest, listed bool) (CycleResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := CycleResult{Records: &protocol.StorageRecords{Version: protocol.StorageIRVersion}, Surfaces: map[protocol.Surface]protocol.SurfaceStatus{protocol.LocalStorage: {State: protocol.Complete}, protocol.IndexedDB: {State: protocol.Complete}}}
	active := map[string]bool{}
	for _, request := range requests {
		active[originKey(request.Origin, request.Container)] = true
	}
	for k := range s.cache {
		if !active[k] {
			delete(s.cache, k)
		}
	}
	var lsBytes, idbBytes int
	wireBytes := 64 // StorageRecords wrapper and delimiters.
	order := append([]OriginRequest(nil), requests...)
	sort.SliceStable(order, func(i, j int) bool {
		a, b := s.cache[originKey(order[i].Origin, order[i].Container)], s.cache[originKey(order[j].Origin, order[j].Container)]
		aComplete := completeOrDisabled(a.Data.LocalStorageStatus) && completeOrDisabled(a.Data.IndexedDBStatus)
		bComplete := completeOrDisabled(b.Data.LocalStorageStatus) && completeOrDisabled(b.Data.IndexedDBStatus)
		if aComplete != bComplete {
			return !aComplete
		}
		if a.AttemptedAt.Equal(b.AttemptedAt) {
			return originKey(order[i].Origin, order[i].Container) < originKey(order[j].Origin, order[j].Container)
		}
		return a.AttemptedAt.Before(b.AttemptedAt)
	})
	budget := s.CycleBudget
	if budget <= 0 || budget > 30*time.Second {
		budget = 30 * time.Second
	}
	cycle, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	for _, request := range order {
		k := originKey(request.Origin, request.Container)
		cached, exists := s.cache[k]
		data := cached.Data
		if !exists {
			data = protocol.StorageOrigin{Origin: request.Origin, Container: request.Container, LocalStorageStatus: incomplete("not read"), IndexedDBStatus: incomplete("not read")}
		}
		switch {
		case !request.Representable:
			data.LocalStorageStatus = incomplete(request.Reason)
			data.IndexedDBStatus = incomplete(request.Reason)
		case request.Due || !exists || !completeOrDisabled(data.LocalStorageStatus) || !completeOrDisabled(data.IndexedDBStatus):
			if cycle.Err() != nil {
				data.LocalStorageStatus = incomplete("cycle budget; read queued")
				data.IndexedDBStatus = incomplete("cycle budget; read queued")
				break
			}
			cached.AttemptedAt = time.Now()
			next, err := s.ReadOrigin(cycle, companion, broker, request)
			outcome := ReadOutcome{Origin: request.Origin, Container: request.Container, Err: err}
			if err != nil {
				data.LocalStorageStatus = incomplete(err.Error())
				data.IndexedDBStatus = incomplete(err.Error())
			} else {
				data = mergeOrigin(data, next)
				if !completeOrDisabled(data.LocalStorageStatus) || !completeOrDisabled(data.IndexedDBStatus) {
					outcome.Err = errors.New("storage origin incomplete")
				} else {
					outcome.Empty = len(data.LocalStorage) == 0 && len(data.IndexedDB) == 0
				}
				cached.ReadAt = time.Now()
			}
			result.Outcomes = append(result.Outcomes, outcome)
		}
		lsSize, lsErr := JSONSize(data.LocalStorage, s.surfaceLimit-lsBytes)
		idbSize, idbErr := JSONSize(data.IndexedDB, s.surfaceLimit-idbBytes)
		wireSize, wireErr := JSONSize(data, s.wireLimit-wireBytes)
		if lsErr != nil || idbErr != nil || wireErr != nil {
			reason := "aggregate storage budget; origin queued and previous sink data retained"
			data.LocalStorage = nil
			data.IndexedDB = nil
			data.LocalStorageStatus = incomplete(reason)
			data.IndexedDBStatus = incomplete(reason)
			result.Outcomes = append(result.Outcomes, ReadOutcome{Origin: request.Origin, Container: request.Container, Err: errors.New(reason)})
			lsSize, idbSize = 0, 0
			wireSize, wireErr = JSONSize(data, s.wireLimit-wireBytes)
		}
		cached.Data = data
		if wireErr == nil {
			s.cache[k] = cached
			result.Records.Origins = append(result.Records.Origins, data)
			lsBytes += lsSize
			idbBytes += idbSize
			wireBytes += wireSize + 1
		} else {
			delete(s.cache, k)
			result.Surfaces[protocol.LocalStorage] = incomplete("aggregate origin metadata budget")
			result.Surfaces[protocol.IndexedDB] = incomplete("aggregate origin metadata budget")
		}

		if !completeOrDisabled(data.LocalStorageStatus) {
			result.Surfaces[protocol.LocalStorage] = incomplete("unread or incomplete origins")
		}
		if !completeOrDisabled(data.IndexedDBStatus) {
			result.Surfaces[protocol.IndexedDB] = incomplete("unread or incomplete origins")
		}
	}
	if !listed {
		result.Surfaces[protocol.LocalStorage] = incomplete("origin inventory unavailable")
		result.Surfaces[protocol.IndexedDB] = incomplete("origin inventory unavailable")
	}
	if s.Surfaces != nil {
		if !s.Surfaces.LocalStorage {
			result.Surfaces[protocol.LocalStorage] = protocol.SurfaceStatus{State: protocol.Unsupported, Reason: "storage read disabled"}
		}
		if !s.Surfaces.IndexedDB {
			result.Surfaces[protocol.IndexedDB] = protocol.SurfaceStatus{State: protocol.Unsupported, Reason: "storage read disabled"}
		}
	}
	if err := s.save(); err != nil {
		return result, err
	}
	return result, nil
}
