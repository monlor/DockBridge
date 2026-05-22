package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Session struct {
	ID              string        `json:"id"`
	LocalRoot       string        `json:"localRoot"`
	RemoteTarget    string        `json:"remoteTarget"`
	RemoteWorkspace string        `json:"remoteWorkspace"`
	AutoRemove      bool          `json:"autoRemove,omitempty"`
	Syncs           []SyncState   `json:"syncs,omitempty"`
	Tunnels         []TunnelState `json:"tunnels,omitempty"`
	GeneratedFiles  []string      `json:"generatedFiles,omitempty"`
	UpdatedAt       time.Time     `json:"updatedAt"`
}

type SyncState struct {
	ID                string `json:"id"`
	LocalPath         string `json:"localPath"`
	RemotePath        string `json:"remotePath"`
	Active            bool   `json:"active"`
	Backend           string `json:"backend,omitempty"`
	MutagenName       string `json:"mutagenName,omitempty"`
	MutagenIdentifier string `json:"mutagenIdentifier,omitempty"`
	RemoteEndpoint    string `json:"remoteEndpoint,omitempty"`
	LastStatus        string `json:"lastStatus,omitempty"`
}

type TunnelState struct {
	ID         string `json:"id"`
	LocalBind  string `json:"localBind"`
	LocalPort  int    `json:"localPort"`
	RemotePort int    `json:"remotePort"`
	Active     bool   `json:"active"`
	PID        int    `json:"pid,omitempty"`
}

type Store struct {
	root string
}

func Identity(localRoot, remoteTarget, composeProject string) string {
	input := strings.Join([]string{filepath.Clean(localRoot), remoteTarget, composeProject}, "\x00")
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])[:16]
}

func WorkspacePath(remoteRoot, id string) string {
	return filepath.ToSlash(filepath.Join(remoteRoot, id))
}

func NewStore(root string) Store {
	return Store{root: root}
}

func (s Store) Save(session Session) error {
	session.UpdatedAt = time.Now().UTC()
	if err := os.MkdirAll(s.sessionsDir(), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path(session.ID), data, 0o600)
}

func (s Store) Load(id string) (Session, error) {
	data, err := os.ReadFile(s.path(id))
	if err != nil {
		return Session{}, err
	}
	var session Session
	if err := json.Unmarshal(data, &session); err != nil {
		return Session{}, err
	}
	return session, nil
}

func (s Store) List() ([]Session, error) {
	entries, err := os.ReadDir(s.sessionsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	sessions := make([]Session, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		sessionPath := filepath.Join(s.sessionsDir(), entry.Name())
		data, err := os.ReadFile(sessionPath)
		if err != nil {
			return nil, fmt.Errorf("read session state %s: %w", sessionPath, err)
		}
		var session Session
		if err := json.Unmarshal(data, &session); err != nil {
			return nil, fmt.Errorf("invalid session state %s: %w", sessionPath, err)
		}
		sessions = append(sessions, session)
	}
	sort.SliceStable(sessions, func(i, j int) bool {
		return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
	})
	return sessions, nil
}

func (s Store) Delete(id string) error {
	err := os.Remove(s.path(id))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (s Store) CleanupManagedState(id string) error {
	return s.CleanupManagedStateWithTerminator(id, nil)
}

func (s Store) CleanupManagedStateWithTerminator(id string, terminate func(SyncState) error) error {
	session, err := s.Load(id)
	if err != nil {
		return err
	}
	if terminate != nil {
		for _, sync := range session.Syncs {
			if sync.Backend == "mutagen" && sync.Active {
				if err := terminate(sync); err != nil {
					return err
				}
			}
		}
	}
	session.Syncs = nil
	session.Tunnels = nil
	session.GeneratedFiles = nil
	return s.Save(session)
}

func (s Store) sessionsDir() string {
	return filepath.Join(s.root, "sessions")
}

func (s Store) path(id string) string {
	return filepath.Join(s.sessionsDir(), id+".json")
}
