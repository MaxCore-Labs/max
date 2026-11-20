package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"max/internal/app/tools"
	"max/internal/domain"
)

type activeRunEntry struct {
	caller  tools.Caller
	token   string
	rootPID int
}

var (
	defaultMu  sync.RWMutex
	defaultSrv *Server
)

// SetDefaultServer sets the package-level Server consulted by ResolveCaller.
func SetDefaultServer(s *Server) {
	defaultMu.Lock()
	defaultSrv = s
	defaultMu.Unlock()
}

// ResolveCaller resolves a verified tools.Caller from the default Server using token and/or OS peerPID.
func ResolveCaller(ctx context.Context, token string, peerPID int) (tools.Caller, error) {
	defaultMu.RLock()
	srv := defaultSrv
	defaultMu.RUnlock()
	if srv == nil {
		return tools.Caller{}, errors.New("mcpserver default server not initialized")
	}
	return srv.ResolveCaller(ctx, token, peerPID)
}

// RegisterRun binds an active run's Caller, optional run token, and optional root OS PID.
func (s *Server) RegisterRun(c tools.Caller, token string, rootPID int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := activeRunEntry{caller: c, token: token, rootPID: rootPID}
	s.byRun[c.RunID] = entry
	if token != "" {
		s.byToken[token] = c.RunID
	}
	if rootPID > 0 {
		s.byRootPID[rootPID] = c.RunID
	}
}

// UnregisterRun removes run token and PID bindings immediately when a run ends.
func (s *Server) UnregisterRun(runID domain.ID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.byRun[runID]
	if !ok {
		return
	}
	delete(s.byRun, runID)
	if entry.token != "" {
		delete(s.byToken, entry.token)
	}
	if entry.rootPID > 0 {
		delete(s.byRootPID, entry.rootPID)
	}
}

// ResolveCaller enforces two-factor caller identity: if both token and peerPID are present,
// they must resolve to the exact same run; if token is absent, peerPID's ancestor tree must match an active run.
func (s *Server) ResolveCaller(_ context.Context, token string, peerPID int) (tools.Caller, error) {
	s.mu.RLock()
	var (
		tokEntry activeRunEntry
		hasTok   bool
	)
	if token != "" {
		if runID, ok := s.byToken[token]; ok {
			tokEntry, hasTok = s.byRun[runID]
		}
	}
	rootMap := make(map[int]activeRunEntry, len(s.byRootPID))
	for pid, runID := range s.byRootPID {
		if e, ok := s.byRun[runID]; ok {
			rootMap[pid] = e
		}
	}
	s.mu.RUnlock()

	if token != "" && !hasTok {
		return tools.Caller{}, errors.New("invalid or expired run token")
	}

	var (
		treeEntry activeRunEntry
		hasTree   bool
	)
	if peerPID > 0 && len(rootMap) > 0 {
		treeEntry, hasTree = matchAncestorRun(peerPID, rootMap)
	}

	if hasTok {
		if peerPID > 0 {
			if hasTree && treeEntry.caller.RunID != tokEntry.caller.RunID {
				return tools.Caller{}, fmt.Errorf(
					"cross-run token rejected: token belongs to %s but peer PID %d belongs to %s",
					tokEntry.caller.RunID, peerPID, treeEntry.caller.RunID,
				)
			}
			if tokEntry.rootPID > 0 && !hasTree {
				return tools.Caller{}, fmt.Errorf(
					"peer PID %d is not a descendant of run %s root PID %d",
					peerPID, tokEntry.caller.RunID, tokEntry.rootPID,
				)
			}
		}
		return tokEntry.caller, nil
	}

	if peerPID > 0 && hasTree {
		return treeEntry.caller, nil
	}
	return tools.Caller{}, errors.New("unable to resolve active run from token or process tree")
}

func matchAncestorRun(startPID int, roots map[int]activeRunEntry) (activeRunEntry, bool) {
	parents := snapshotProcessParents()
	visited := make(map[int]bool, 16)
	cur := startPID
	for cur > 0 && !visited[cur] {
		visited[cur] = true
		if entry, ok := roots[cur]; ok {
			return entry, true
		}
		next, ok := parents[cur]
		if !ok {
			p, pok := lookupSingleParentPID(cur)
			if !pok {
				break
			}
			next = p
		}
		cur = next
	}
	return activeRunEntry{}, false
}
