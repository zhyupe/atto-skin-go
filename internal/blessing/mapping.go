package blessing

import (
	"crypto/md5" // Minecraft's OfflinePlayer UUID algorithm, not password hashing.
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var mappingHeader = []string{"action", "uid", "pid", "source_name", "email", "permission", "profile_uuid", "uuid_source", "oidc_subject", "name"}

type Mapping struct {
	Action                          string
	UID, PID                        int64
	SourceName, UUID, Subject, Name string
}

var compactUUID = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)
var dashedUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func normalizeUUID(raw string) (string, error) {
	if !compactUUID.MatchString(raw) && !dashedUUID.MatchString(raw) {
		return "", errors.New("UUID must be 32 hex digits or standard hyphenated form")
	}
	value := strings.ToLower(strings.ReplaceAll(raw, "-", ""))
	if value == strings.Repeat("0", 32) {
		return "", errors.New("nil UUID is not a player identity")
	}
	return value, nil
}
func offlineUUID(name string) string {
	b := md5.Sum([]byte("OfflinePlayer:" + name))
	b[6] = (b[6] & 15) | 48
	b[8] = (b[8] & 63) | 128
	return hex.EncodeToString(b[:])
}

// ResolveUUID never invents a random replacement for an unknown existing identity.
// Stored UUIDs take precedence, including Mojang-verification overrides.
func (s *Snapshot) ResolveUUID(p Player) (string, string) {
	var candidates []UUIDRecord
	for _, r := range s.UUIDs {
		if (s.UUIDByPID && r.PID.Valid && r.PID.Int64 == p.PID) || (!s.UUIDByPID && strings.EqualFold(r.Name, p.Name)) {
			candidates = append(candidates, r)
		}
	}
	// Connect uses PID; orphaned records with matching names need explicit review.
	if s.UUIDByPID && len(candidates) == 0 {
		for _, r := range s.UUIDs {
			if strings.EqualFold(r.Name, p.Name) {
				return "", "review-orphaned-uuid"
			}
		}
	}
	if len(candidates) > 1 {
		return "", "review-duplicate-uuid"
	}
	if len(candidates) == 1 {
		u, err := normalizeUUID(candidates[0].UUID)
		if err != nil {
			return "", "review-invalid-uuid"
		}
		if candidates[0].Name != p.Name {
			return "", "review-renamed-uuid"
		}
		return u, "uuid-table"
	}
	if s.Algorithm == "v3" {
		return offlineUUID(p.Name), "offline-v3"
	}
	return "", "manual-required"
}

// WriteTemplate creates a private new file; it refuses to overwrite an edited mapping.
func WriteTemplate(path string, s *Snapshot) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(path)
		}
	}()
	w := csv.NewWriter(f)
	if err = w.Write(mappingHeader); err != nil {
		return err
	}
	for _, p := range s.Players {
		a, exists := s.Accounts[p.UID]
		if !exists {
			return fmt.Errorf("player %d refers to missing account %d", p.PID, p.UID)
		}
		uuid, source := s.ResolveUUID(p)
		action := "pending"
		if a.Permission < 0 {
			action = "skip"
		}
		if err = w.Write([]string{action, strconv.FormatInt(p.UID, 10), strconv.FormatInt(p.PID, 10), p.Name, a.Email, strconv.Itoa(a.Permission), uuid, source, "", p.Name}); err != nil {
			return err
		}
	}
	w.Flush()
	if err = w.Error(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}
func ReadMapping(r io.Reader) ([]Mapping, error) {
	reader := csv.NewReader(r)
	header, err := reader.Read()
	if err != nil {
		return nil, err
	}
	// Accept the UTF-8 BOM emitted by some spreadsheet editors.
	if len(header) > 0 {
		header[0] = strings.TrimPrefix(header[0], "\ufeff")
	}
	if len(header) != len(mappingHeader) {
		return nil, errors.New("unexpected mapping header; generate it with -export-mapping")
	}
	for i, h := range header {
		if h != mappingHeader[i] {
			return nil, fmt.Errorf("mapping column %d must be %s", i+1, mappingHeader[i])
		}
	}
	result := []Mapping{}
	for line := 2; ; line++ {
		record, e := reader.Read()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, fmt.Errorf("mapping line %d: %w", line, e)
		}
		uid, e := strconv.ParseInt(record[1], 10, 64)
		if e != nil || uid <= 0 {
			return nil, fmt.Errorf("mapping line %d: invalid uid", line)
		}
		pid, e := strconv.ParseInt(record[2], 10, 64)
		if e != nil || pid <= 0 {
			return nil, fmt.Errorf("mapping line %d: invalid pid", line)
		}
		result = append(result, Mapping{Action: record[0], UID: uid, PID: pid, SourceName: record[3], UUID: record[6], Subject: record[8], Name: record[9]})
	}
	return result, nil
}
